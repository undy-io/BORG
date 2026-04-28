from datetime import datetime
from typing import Any

from fastapi import FastAPI, Request
from fastapi.responses import StreamingResponse

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
        ],
    }


@app.post("/v1/chat/completions")
async def chat_completions(request: Request):
    """
    Return deterministic OpenAI-shaped data for local BORG validation.
    """
    body: Any
    try:
        body = await request.json()
    except ValueError:
        body = None

    wants_stream = (
        isinstance(body, dict)
        and bool(body.get("stream"))
    ) or "text/event-stream" in request.headers.get("accept", "")

    if wants_stream:
        return StreamingResponse(_stream_chunks(), media_type="text/event-stream")

    return {
        "upstream": "dummy-openai",
        "path": request.url.path,
        "auth": request.headers.get("authorization"),
        "content_type": request.headers.get("content-type"),
        "body": body,
    }


async def _stream_chunks():
    chunks = [
        'data: {"id":"dummy","choices":[{"delta":{"content":"Hello"}}]}\n\n',
        'data: {"id":"dummy","choices":[{"delta":{"content":" from KinD"}}]}\n\n',
        "data: [DONE]\n\n",
    ]
    for chunk in chunks:
        yield chunk
