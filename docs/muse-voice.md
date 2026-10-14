# Muse Voice Transcribe integration

## Implemented scope

The image provides `shelley-transcribe` for local recordings, using Meta's multipart file API and returning the transcript on stdout.
This is an opt-in replacement for sending a recording to OpenAI through an agent tool.
It is not a replacement for Shelley's built-in recording or live-conversation UI.
The default provider endpoint is `https://api.meta.ai/v1/asr/transcribe`, with model `muse-voice-transcribe-1.0`.
The helper converts inputs to mono 24 kHz PCM WAV and rejects recordings longer than ten minutes before uploading.
These formats, endpoint, model, authorization header, request fields, response fields, and size limits come from [Meta's speech-to-text documentation](https://dev.meta.ai/docs/speech-to-text).

## Why the live button cannot use this API directly

The pinned Shelley release sends an SDP offer to a live-session endpoint, selecting `gpt-live-1` with client delegation, and expects a session ID plus an SDP answer.
Its default endpoint is an exe.dev service, and its explicit direct-provider override targets OpenAI's live-session API.
See [Shelley's live-session implementation at the pinned release](https://github.com/boldsoftware/shelley/blob/v0.1363.920203221/server/live_session.go).

Muse's streaming endpoint instead accepts a JSON handshake followed by raw mono PCM audio over a WebSocket, then returns transcription events.
It does not implement Shelley's SDP session or delegation protocol.
See [Meta's available endpoints and session lifecycle](https://dev.meta.ai/docs/speech-to-text).
A full live-voice implementation needs audio capture, turn handling, forwarding transcripts into Shelley, response playback, and interruption handling.
Changing only the provider URL or model name cannot provide that functionality.

## Why the recording UI also needs an adapter

Shelley's recording transcriber uses OpenAI multipart requests and response schemas.
Its availability check requires both text transcription and a timestamp model, and review recordings request word and segment timestamps.
See [Shelley's recording transcriber](https://github.com/boldsoftware/shelley/blob/v0.1363.920203221/server/openai_transcription.go).
Meta uses a different request schema and documents speech turns rather than OpenAI's word-timing response schema.
The current helper deliberately does not fabricate those timestamps or register Muse under an OpenAI model name.

## Remaining decision

The original built-in voice replacement remains open.
Choose whether the next integration should target dictation, recording review, or a complete spoken conversation.
A spoken conversation also needs a response-playback choice, such as browser speech synthesis or a separate speech provider.
A real Meta key and an explicit, potentially billed acceptance test are required to verify provider access.
No live Meta request was made during implementation.
