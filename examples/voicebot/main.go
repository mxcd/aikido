// voicebot is the chatbot example with a voice: an agent over an embedded
// markdown corpus (a small German bathroom-planning knowledge base), served
// as a web page with push-to-talk, a text box and spoken answers.
//
// All four combinations of voice or text in and text or voice out run through
// one voice.Pipeline and one POST /api/turn endpoint that streams NDJSON.
//
//	go run ./examples/voicebot
//
// Then open http://localhost:8080. Set OPENROUTER_API_KEY first; ADDR
// overrides the listen address. The page and its script are embedded, there
// is no build step. One conversation per process, shared by every tab.
package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"

	"github.com/mxcd/aikido/agent"
	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/openrouter"
	"github.com/mxcd/aikido/tools"
	"github.com/mxcd/aikido/vfs/embedfs"
	"github.com/mxcd/aikido/voice"
)

//go:embed knowledge/*.md
var knowledgeFS embed.FS

//go:embed index.html
var indexHTML []byte

const systemPrompt = `Du bist ein Badplaner. In deinem Arbeitsbereich liegen Markdown-Dateien mit ` +
	`Planungswissen und Produkten. Nutze list_files, search und read_file, bevor du antwortest, ` +
	`und antworte nur mit dem, was dort steht. Steht es nicht dort, sag das offen. Antworte in der ` +
	`Sprache des Nutzers.`

// vocabulary is what the speech recognizer should spell exactly: brand,
// series and article numbers from the corpus, plus trade words.
var vocabulary = &llm.Vocabulary{
	Terms: []string{"Liv", "PFEIFFER & MAY", "LIV60G", "LIV80W", "LIV80U", "Duschtasse", "Waschtisch", "Unterschrank", "Vorwandinstallation", "Walk-in-Dusche"},
	Replacements: map[string]string{
		"pfeifer und mai":  "PFEIFFER & MAY",
		"pfeiffer und mai": "PFEIFFER & MAY",
		"pfeiffer und may": "PFEIFFER & MAY",
	},
	Fuzzy: true,
}

func main() {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENROUTER_API_KEY not set")
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "localhost:8080"
	}

	client, err := openrouter.NewClient(&openrouter.Options{
		APIKey:      apiKey,
		HTTPReferer: "https://github.com/mxcd/aikido",
		XTitle:      "aikido voicebot example",
	})
	if err != nil {
		log.Fatal(err)
	}

	knowledge, err := fs.Sub(knowledgeFS, "knowledge")
	if err != nil {
		log.Fatal(err)
	}
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
		Storage:  embedfs.NewStorage(knowledge),
		ReadOnly: true,
	}); err != nil {
		log.Fatal(err)
	}
	session, err := agent.NewLocalSession(&agent.SessionOptions{
		ID:           "voicebot",
		Client:       client,
		Tools:        registry,
		Model:        "anthropic/claude-haiku-4.5",
		SystemPrompt: systemPrompt,
		MaxTurns:     10,
	})
	if err != nil {
		log.Fatal(err)
	}

	pipeline, err := voice.New(&voice.Options{
		Transcriber:      client,
		Vocabulary:       vocabulary,
		Speech:           client,
		SpeechModel:      voice.DefaultSpeechModel, // elevenlabs/eleven-v4-turbo
		Voice:            "sarah",
		SpeechUSDPerChar: 0.00002,
		SpokenStyle:      voice.DefaultSpokenStyle,
	})
	if err != nil {
		log.Fatal(err)
	}
	turn, err := voice.NewHandler(&voice.HandlerOptions{
		Pipeline: pipeline,
		Session:  func(*http.Request) (voice.Runner, error) { return session, nil },
	})
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("POST /api/turn", turn)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	log.Printf("voicebot on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
