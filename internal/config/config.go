package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// Eva
	EvaRPCURL          string
	EvaAPIToken        string
	EvaAPITokenHeader  string
	EvaAPITokenScheme  string
	EvaAuthHeader      string
	EvaLogin           string
	EvaPassword        string
	EvaAuthLoginURL    string
	TaskListMethod     string
	TaskGetMethod      string
	TaskCommentsMethod string
	TaskLinkMethod     string
	TaskIDField        string
	TaskTitleField     string
	TaskDescField      string
	TaskResultField    string
	TaskCommentField   string
	TaskPayloadFields  []string

	// Embeddings
	EmbedProvider string // ollama | tei
	OllamaURL     string
	OllamaModel   string
	TEIURL        string
	EmbedDim      int
	EmbedMaxChars int

	// Qdrant
	QdrantURL        string
	QdrantCollection string
	QdrantDistance   string

	// Indexer
	IndexerPageSize  int
	IndexerBatchSize int
	IndexerRecreate  bool

	// Listener
	ListenerAddr           string
	ListenerWebhookPath    string
	ListenerTaskIDPath     string
	ListenerScoreThreshold float64
	ListenerMaxLinks       int
	LinksDryRun            bool
	WebhookToken           string

	LogLevel string
}

func Load() Config {
	LoadEnvFile(".env")
	cfg := Config{
		EvaRPCURL:              get("EVA_RPC_URL", "http://localhost:8080/api"),
		EvaAuthHeader:          get("EVA_AUTH_HEADER", ""),
		EvaLogin:               get("EVA_LOGIN", ""),
		EvaPassword:            get("EVA_PASSWORD", ""),
		EvaAuthLoginURL:        get("EVA_AUTH_LOGIN_URL", ""),
		EvaAPIToken:            get("EVA_API_TOKEN", ""),
		TaskListMethod:         get("EVA_TASK_LIST_METHOD", "CmfTask.list"),
		TaskGetMethod:          get("EVA_TASK_GET_METHOD", "CmfTask.get"),
		TaskCommentsMethod:     get("EVA_TASK_COMMENTS_METHOD", "CmfTaskComment.list"),
		TaskLinkMethod:         get("EVA_TASK_LINK_METHOD", "CmfTask.save_links"),
		TaskIDField:            get("EVA_TASK_ID_FIELD", "id"),
		TaskTitleField:         get("EVA_TASK_TITLE_FIELD", "title"),
		TaskDescField:          get("EVA_TASK_DESC_FIELD", "description"),
		TaskResultField:        get("EVA_TASK_RESULT_FIELD", "result"),
		TaskCommentField:       get("EVA_TASK_COMMENT_FIELD", "text"),
		TaskPayloadFields:      split(get("EVA_TASK_PAYLOAD_FIELDS", "number,project_id,status")),
		EmbedProvider:          get("EMBED_PROVIDER", "ollama"),
		OllamaURL:              strings.TrimRight(get("OLLAMA_URL", "http://localhost:11434"), "/"),
		OllamaModel:            get("OLLAMA_MODEL", "nomic-embed-text"),
		TEIURL:                 strings.TrimRight(get("TEI_URL", "http://localhost:8081"), "/"),
		EmbedDim:               getInt("EMBED_DIM", 768),
		EmbedMaxChars:          getInt("EMBED_MAX_CHARS", 6000),
		QdrantURL:              strings.TrimRight(get("QDRANT_URL", "http://localhost:46333"), "/"),
		QdrantCollection:       get("QDRANT_COLLECTION", "eva_tasks"),
		QdrantDistance:         get("QDRANT_DISTANCE", "Cosine"),
		IndexerPageSize:        getInt("INDEXER_PAGE_SIZE", 100),
		IndexerBatchSize:       getInt("INDEXER_BATCH_SIZE", 16),
		IndexerRecreate:        getBool("INDEXER_RECREATE", false),
		ListenerAddr:           get("LISTENER_ADDR", ":8080"),
		ListenerWebhookPath:    get("LISTENER_WEBHOOK_PATH", "/webhook/task"),
		ListenerTaskIDPath:     get("LISTENER_TASK_ID_PATH", "id"),
		ListenerScoreThreshold: getFloat("LISTENER_SCORE_THRESHOLD", 0.75),
		ListenerMaxLinks:       getInt("LISTENER_MAX_LINKS", 5),
		LinksDryRun:            getBool("LINKS_DRY_RUN", true),
		WebhookToken:           get("WEBHOOK_TOKEN", ""),
		LogLevel:               get("LOG_LEVEL", "info"),
	}
	return cfg
}

func get(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func getFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func split(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
