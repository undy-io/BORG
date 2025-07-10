from fastapi import FastAPI
from datetime import datetime

app = FastAPI(title="Dummy OpenAI")

MODEL_ID = "gpt-3.5-turbo"

@app.get("/v1/models")
async def list_models():
    """
    Return a minimal OpenAI-compatible list response.
    """
    return {
        "object": "list",
        "data": [
            {
                "id": MODEL_ID,
                "object": "model",
                "created": int(datetime.utcnow().timestamp()),
                "owned_by": "dummy",
            }
        ]
    }
