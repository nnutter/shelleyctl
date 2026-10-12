package startup

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type launcher struct{ path, data string }

func render(a *Adapter, name, executable, stateDir string) []launcher {
	if a.platform == "linux" {
		service := fmt.Sprintf(`# %s
[Unit]
Description=Resume Shelley instance %s

[Service]
Type=oneshot
ExecStart=:/usr/bin/env -- %s --plain create %s
Environment=%s
Environment="SHELLEYCTL_RESUME=1"
Environment=%s
TimeoutStartSec=0
`, marker, name, unitQuote(executable), name, unitQuote("SHELLEYCTL_STATE_DIR="+stateDir), unitQuote("PATH="+os.Getenv("PATH")))
		timer := fmt.Sprintf(`# %s
[Unit]
Description=Periodically resume Shelley instance %s

[Timer]
OnStartupSec=1s
OnUnitInactiveSec=30s
AccuracySec=1s
Unit=%s.service

[Install]
WantedBy=timers.target
`, marker, name, unit(name))
		// Timer first: Remove must disable future invocations before stopping CLI.
		return []launcher{{filepath.Join(a.dir, unit(name)+".timer"), timer}, {filepath.Join(a.dir, unit(name)+".service"), service}}
	}
	plist := fmt.Sprintf(`<!-- %s -->
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array>
<string>%s</string><string>--plain</string><string>create</string><string>%s</string>
</array>
<key>EnvironmentVariables</key><dict>
<key>SHELLEYCTL_STATE_DIR</key><string>%s</string>
<key>SHELLEYCTL_RESUME</key><string>1</string>
<key>PATH</key><string>%s</string>
</dict>
<key>RunAtLoad</key><true/>
<key>StartInterval</key><integer>30</integer>
</dict></plist>
`, marker, xmlText(label(name)), xmlText(executable), xmlText(name), xmlText(stateDir), xmlText(os.Getenv("PATH")))
	return []launcher{{filepath.Join(a.dir, label(name)+".plist"), plist}}
}

func unitQuote(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`)
	return `"` + replacer.Replace(value) + `"`
}

func xmlText(value string) string {
	var b bytes.Buffer
	// bytes.Buffer writes cannot fail.
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}
