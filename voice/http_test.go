package voice

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
)

func newTestHandler(t *testing.T, stt, chat llm.Client, maxBytes int64) (*Handler, func() []llm.Message) {
	t.Helper()
	p, err := New(&Options{Transcriber: stt, Speech: &fakeSpeech{}})
	if err != nil {
		t.Fatal(err)
	}
	session, hist := newSession(t, chat)
	h, err := NewHandler(&HandlerOptions{
		Pipeline:      p,
		Session:       func(*http.Request) (Runner, error) { return session, nil },
		MaxAudioBytes: maxBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, func() []llm.Message {
		msgs, _ := hist.Read(context.Background(), "voice-test")
		return msgs
	}
}

func readLines(t *testing.T, body *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %q is not JSON: %v", sc.Text(), err)
		}
		lines = append(lines, m)
	}
	return lines
}

func TestHandler_TextInSpeechOut_NDJSON(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, nil, llmtest.NewStubClient(replyTurn(0.002, answer1+answer2)), 0)
	req := httptest.NewRequest(http.MethodPost, "/voice", strings.NewReader(`{"text":"Hallo","output":"speech"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("status %d, type %q: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	lines := readLines(t, rec.Body)
	var types []string
	for _, l := range lines {
		types = append(types, l["type"].(string))
	}
	if got := strings.Join(types, ","); got != "text,speech,speech,done" {
		t.Errorf("types = %s", got)
	}
	sp := lines[1]
	audio, _ := base64.StdEncoding.DecodeString(sp["audio"].(string))
	if sp["seq"] != float64(0) || sp["content_type"] != "audio/mpeg" || string(audio) != "audio:"+strings.TrimSpace(answer1) {
		t.Errorf("speech line = %v", sp)
	}
	done := lines[len(lines)-1]
	usage := done["usage"].(map[string]any)
	if done["end_reason"] != "stop" || usage["llm_cost_usd"] != 0.002 || usage["speech_chars"] == float64(0) {
		t.Errorf("done line = %v", done)
	}
}

func TestHandler_AudioUploads(t *testing.T) {
	t.Parallel()
	multipartBody := func() (*bytes.Buffer, string) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="audio"; filename="clip.webm"`)
		hdr.Set("Content-Type", "audio/webm;codecs=opus")
		part, _ := mw.CreatePart(hdr)
		_, _ = part.Write([]byte("webm-bytes"))
		_ = mw.WriteField("output", "text")
		_ = mw.WriteField("language", "de")
		_ = mw.Close()
		return &b, mw.FormDataContentType()
	}
	for name, build := range map[string]func() *http.Request{
		"multipart": func() *http.Request {
			b, ct := multipartBody()
			r := httptest.NewRequest(http.MethodPost, "/voice", b)
			r.Header.Set("Content-Type", ct)
			return r
		},
		"raw body": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/voice?output=text&language=de", strings.NewReader("webm-bytes"))
			r.Header.Set("Content-Type", "audio/webm;codecs=opus")
			return r
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stt := llmtest.NewStubClient(transcriptTurn("Wo kommt die Dusche hin?"))
			h, _ := newTestHandler(t, stt, llmtest.NewStubClient(replyTurn(0, "An die Wand.")), 0)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, build())
			lines := readLines(t, rec.Body)
			if rec.Code != 200 || len(lines) < 3 || lines[0]["type"] != "transcript" || lines[0]["text"] != "Wo kommt die Dusche hin?" {
				t.Fatalf("status %d, lines %v", rec.Code, lines)
			}
			audio := stt.Requests()[0].Messages[0].Audio[0]
			if string(audio.Data) != "webm-bytes" || audio.Format != "webm" {
				t.Errorf("stt got %+v", audio)
			}
		})
	}
}

func TestHandler_RejectsBadRequests(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, llmtest.NewStubClient(), llmtest.NewStubClient(), 64)
	for name, tc := range map[string]struct {
		method, ct, body string
		want             int
	}{
		"GET":            {http.MethodGet, "", "", http.StatusMethodNotAllowed},
		"too large":      {http.MethodPost, "audio/webm", strings.Repeat("x", 65), http.StatusRequestEntityTooLarge},
		"unknown audio":  {http.MethodPost, "audio/midi", "x", http.StatusUnsupportedMediaType},
		"unknown body":   {http.MethodPost, "text/plain", "hi", http.StatusUnsupportedMediaType},
		"empty text":     {http.MethodPost, "application/json", `{"text":""}`, http.StatusBadRequest},
		"unknown output": {http.MethodPost, "application/json", `{"text":"x","output":"video"}`, http.StatusBadRequest},
	} {
		req := httptest.NewRequest(tc.method, "/voice", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.ct)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body, tc.want)
		}
	}
}

// A client that disconnects after the first line must not cancel the turn:
// the reply still lands in History.
func TestHandler_ClientDisconnectDoesNotCancelTheTurn(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	chat := &gatedClient{first: "Die Dusche passt an die Fensterwand. ", rest: "Der Rest kommt später.", gate: release}
	h, history := newTestHandler(t, nil, chat, 0)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(`{"text":"Hallo"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if !strings.Contains(line, `"type":"text"`) {
		t.Fatalf("first line = %q", line)
	}
	cancel()
	_ = resp.Body.Close()
	time.Sleep(50 * time.Millisecond) // let the server notice the disconnect
	close(release)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if msgs := history(); len(msgs) == 2 {
			if want := "Die Dusche passt an die Fensterwand. Der Rest kommt später."; msgs[1].Content != want {
				t.Fatalf("assistant = %q", msgs[1].Content)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("turn did not complete after the client disconnected")
}
