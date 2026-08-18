package gemini

import "encoding/json"

// Wire types for the Gemini generateContent API.

type generateRequest struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	Tools             []wireTool        `json:"tools,omitempty"`
	ToolConfig        *toolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
	// safetySettings is deliberately absent. It is Gemini-only and Polyglot
	// has nothing to do with it, so leaving it unnamed lets the generic
	// extension capture carry it back to a Gemini upstream unchanged, and
	// report it on any other route.
}

type content struct {
	Role  string `json:"role,omitempty"` // user | model
	Parts []part `json:"parts"`
}

// part is Gemini's content union. Only one field is set per part.
type part struct {
	Text string `json:"text,omitempty"`
	// Thought marks a text part as reasoning rather than answer.
	Thought bool `json:"thought,omitempty"`
	// ThoughtSignature is the opaque token that lets a thought be replayed.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`

	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`

	InlineData *inlineData `json:"inlineData,omitempty"`
	FileData   *fileData   `json:"fileData,omitempty"`
}

// inlineData is Gemini's attachment: base64 bytes with their type.
type inlineData struct {
	MIMEType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

// fileData points at a file already uploaded to Google. The URI is meaningful
// only to Gemini, so it travels like any other provider-bound handle.
type fileData struct {
	MIMEType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri,omitempty"`
}

type functionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type functionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty"`
}

type wireTool struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations,omitempty"`
	// Server-side tools — googleSearch, codeExecution, urlContext and whatever
	// Google adds next — are not named here on purpose. They are taken from
	// the raw entry instead, so a tool this struct has never heard of is still
	// carried back to a Gemini upstream rather than silently dropped.
}

type functionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type toolConfig struct {
	FunctionCallingConfig *functionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type functionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"` // AUTO | ANY | NONE
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type generationConfig struct {
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"topP,omitempty"`
	TopK             *int            `json:"topK,omitempty"`
	CandidateCount   *int            `json:"candidateCount,omitempty"`
	MaxOutputTokens  *int            `json:"maxOutputTokens,omitempty"`
	StopSequences    []string        `json:"stopSequences,omitempty"`
	PresencePenalty  *float64        `json:"presencePenalty,omitempty"`
	FrequencyPenalty *float64        `json:"frequencyPenalty,omitempty"`
	Seed             *int64          `json:"seed,omitempty"`
	ResponseMIMEType string          `json:"responseMimeType,omitempty"`
	ResponseSchema   json.RawMessage `json:"responseSchema,omitempty"`
	ThinkingConfig   *thinkingConfig `json:"thinkingConfig,omitempty"`
}

type thinkingConfig struct {
	// ThinkingBudget is a token budget; -1 means "let the model decide".
	ThinkingBudget  *int `json:"thinkingBudget,omitempty"`
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
}

type generateResponse struct {
	Candidates    []candidate    `json:"candidates"`
	UsageMetadata *usageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion  string         `json:"modelVersion,omitempty"`
	ResponseID    string         `json:"responseId,omitempty"`
	// CreateTime is RFC 3339, and is the counterpart of OpenAI's numeric
	// "created". It is named here rather than left to Capture because a field
	// with an exact equivalent in another protocol belongs in canonical: as an
	// extension it would be replayed only on a Gemini→Gemini route and
	// reported as unsupported everywhere else, which is a false alarm.
	CreateTime string `json:"createTime,omitempty"`

	PromptFeedback *struct {
		BlockReason string `json:"blockReason,omitempty"`
	} `json:"promptFeedback,omitempty"`
}

type candidate struct {
	Content      *content `json:"content,omitempty"`
	FinishReason string   `json:"finishReason,omitempty"`
	Index        int      `json:"index"`
}

type usageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
}

type wireError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}
