//go:build smoke

package openrouter

import (
	"bytes"
	"context"
	"encoding/binary"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mxcd/aikido/llm"
)

// TestSmoke_Transcribe sends faint noise, which must come back empty (models
// like to invent a sentence for it), and, where macOS `say` is available, a
// spoken sentence, which must come back as text.
func TestSmoke_Transcribe(t *testing.T) {
	if os.Getenv("AIKIDO_LIVE_SMOKE") != "1" {
		t.Skip("set AIKIDO_LIVE_SMOKE=1 to run the live smoke test")
	}
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY is not set")
	}
	c, err := NewClient(&Options{APIKey: apiKey, XTitle: "aikido-smoke"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	text, usage, err := llm.Transcribe(ctx, c, llm.TranscribeRequest{Audio: noiseWAV(2 * 16000), Format: "wav"})
	if err != nil || text != "" {
		t.Errorf("noise: %q, %v", text, err)
	}
	t.Logf("noise: usage %+v", usage)

	if _, err := exec.LookPath("say"); err != nil {
		t.Log("no `say` on PATH, speech clip skipped")
		return
	}
	path := filepath.Join(t.TempDir(), "speech.wav")
	if out, err := exec.Command("say", "-o", path, "--data-format=LEI16@16000", "The quick brown fox jumps over the lazy dog.").CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}
	clip, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text, _, err = llm.Transcribe(ctx, c, llm.TranscribeRequest{Audio: clip, Format: "wav"})
	if err != nil || !strings.Contains(strings.ToLower(text), "brown fox") {
		t.Errorf("speech: %q, %v", text, err)
	}
	t.Logf("speech: %q", text)
}

// noiseWAV returns n samples of faint 16 kHz mono PCM noise as a WAV file.
func noiseWAV(n int) []byte {
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + 2*n))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))     // PCM
	w(uint16(1))     // mono
	w(uint32(16000)) // sample rate
	w(uint32(32000)) // byte rate
	w(uint16(2))     // block align
	w(uint16(16))    // bits per sample
	b.WriteString("data")
	w(uint32(2 * n))
	r := rand.New(rand.NewPCG(1, 2))
	for range n {
		w(int16(r.IntN(200) - 100))
	}
	return b.Bytes()
}
