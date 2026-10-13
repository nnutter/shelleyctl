package image

import (
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// This native test exercises real curl, ffmpeg, the image entrypoint, and a
// protocol-checking provider. No paid API call or production endpoint override.
func TestNativeMuseTranscription(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the local TLS provider contract uses Podman's host network")
	}
	image := os.Getenv("SHELLEYCTL_TEST_IMAGE")
	if image == "" {
		t.Skip("set SHELLEYCTL_TEST_IMAGE to run the native Podman contract")
	}
	home := t.TempDir()
	keyDir := filepath.Join(home, ".config", "shelley")
	require.NoError(t, os.MkdirAll(keyDir, 0o700))
	keyPath := filepath.Join(keyDir, "muse-api-key")
	require.NoError(t, os.WriteFile(keyPath, []byte("synthetic-meta-key\n"), 0o600))
	observed := make(chan error, 1)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		err := validateMuseUpload(r)
		observed <- err
		if err != nil {
			http.Error(w, "invalid upload", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"transcript":"native Muse transcript"}`)
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	require.NoError(t, err)
	// Curl's normal user configuration redirects only this synthetic test to
	// the local TLS provider. The production helper still names Meta's URL.
	rc := fmt.Sprintf("connect-to = \"api.meta.ai:443:%s\"\ninsecure\nnoproxy = \"*\"\n", endpoint.Host)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".curlrc"), []byte(rc), 0o600))
	output, err := runTranscriptionImage(t, image, home, `
        ffmpeg -nostdin -hide_banner -loglevel error -f lavfi -i sine=frequency=440 -t 0.2 "$HOME/input.webm"
        shelley-transcribe "$HOME/input.webm"
    `)
	require.NoError(t, err, "%s", output)
	require.Equal(t, "native Muse transcript\n", string(output))
	select {
	case err := <-observed:
		require.NoError(t, err)
	default:
		require.FailNow(t, "no Meta request reached the protocol provider")
	}
	require.Equal(t, int32(1), calls.Load())
	require.NoError(t, os.WriteFile(keyPath, []byte("synthetic-meta-key\ninjected-header"), 0o600))
	output, err = runTranscriptionImage(t, image, home, `shelley-transcribe "$HOME/input.webm"`)
	require.Error(t, err)
	require.NotContains(t, string(output), "synthetic-meta-key")
	require.Equal(t, int32(1), calls.Load(), "invalid credentials must not contact the provider")
	require.NoError(t, os.WriteFile(keyPath, []byte("synthetic-meta-key\n"), 0o600))
	output, err = runTranscriptionImage(t, image, home, `
        ffmpeg -nostdin -hide_banner -loglevel error -f lavfi -i anullsrc=r=24000:cl=mono -t 601 "$HOME/long.wav"
        shelley-transcribe "$HOME/long.wav"
    `)
	require.Error(t, err)
	require.Contains(t, string(output), "at most 10 minutes")
	require.Equal(t, int32(1), calls.Load(), "oversized recordings must not create billed requests")
}

func runTranscriptionImage(t *testing.T, image, home, script string) ([]byte, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "podman", "run", "--rm", "--pull=never", "--network=host", "--passwd=false", "--userns=keep-id:uid=1000,gid=1000", "--user", "1000:1000", "--volume", home+":/home/shelley", image, "bash", "-ec", script)
	return cmd.CombinedOutput()
}

func validateMuseUpload(r *http.Request) (result error) {
	if r.Method != http.MethodPost {
		return errors.New("expected POST")
	}
	if r.URL.Path != "/v1/asr/transcribe" {
		return errors.New("unexpected provider path")
	}
	if r.Header.Get("Authorization") != "Bearer synthetic-meta-key" {
		return errors.New("missing bearer authorization")
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, r.MultipartForm.RemoveAll()) }()
	if err := validateMuseRequest(strings.NewReader(r.FormValue("request"))); err != nil {
		return err
	}
	return validateMuseAudio(r)
}

func validateMuseRequest(reader io.Reader) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	var request struct {
		Mode          string `json:"mode"`
		Model         string `json:"model"`
		AudioEncoding string `json:"audioEncoding"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return err
	}
	if request.Mode != "ENDPOINTING" {
		return errors.New("unexpected mode")
	}
	if request.Model != "muse-voice-transcribe-1.0" {
		return errors.New("unexpected model")
	}
	if request.AudioEncoding != "WAV" {
		return errors.New("unexpected encoding")
	}
	return nil
}

func validateMuseAudio(r *http.Request) (result error) {
	audio, _, err := r.FormFile("audio")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, audio.Close()) }()
	header := make([]byte, 36)
	if _, err := io.ReadFull(audio, header); err != nil {
		return err
	}
	if string(header[:4]) != "RIFF" {
		return errors.New("not RIFF")
	}
	if string(header[8:12]) != "WAVE" {
		return errors.New("not WAV")
	}
	if binary.LittleEndian.Uint16(header[20:22]) != 1 {
		return errors.New("not PCM")
	}
	if binary.LittleEndian.Uint16(header[22:24]) != 1 {
		return errors.New("not mono")
	}
	if binary.LittleEndian.Uint32(header[24:28]) != 24000 {
		return errors.New("not 24 kHz")
	}
	if binary.LittleEndian.Uint16(header[34:36]) != 16 {
		return errors.New("not 16-bit")
	}
	return nil
}
