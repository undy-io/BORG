package openai

type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created *int   `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ModelListResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}
