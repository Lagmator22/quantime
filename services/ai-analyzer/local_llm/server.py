from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from typing import List, Optional, Dict, Any
import openvino_genai as ov_genai
import os
import uvicorn
import json

app = FastAPI()

MODEL_PATH = os.getenv("MODEL_PATH", "/models/Llama-3.2-3B-Instruct-INT4")
print(f"Loading OpenVINO model from {MODEL_PATH}...")

try:
    pipe = ov_genai.LLMPipeline(MODEL_PATH, "CPU")
    print("Model loaded successfully.")
except Exception as e:
    print(f"Failed to load model: {e}")
    pipe = None

class Message(BaseModel):
    role: str
    content: str

class ChatCompletionRequest(BaseModel):
    model: str
    messages: List[Message]
    temperature: Optional[float] = 0.7
    max_tokens: Optional[int] = 1024
    response_format: Optional[Dict[str, Any]] = None

@app.post("/v1/chat/completions")
async def chat_completions(req: ChatCompletionRequest):
    if not pipe:
        raise HTTPException(status_code=500, detail="Model not loaded")
    
    # Construct prompt from messages
    prompt = ""
    for msg in req.messages:
        if msg.role == "system":
            prompt += f"<|start_header_id|>system<|end_header_id|>\n\n{msg.content}<|eot_id|>\n"
        elif msg.role == "user":
            prompt += f"<|start_header_id|>user<|end_header_id|>\n\n{msg.content}<|eot_id|>\n"
        elif msg.role == "assistant":
            prompt += f"<|start_header_id|>assistant<|end_header_id|>\n\n{msg.content}<|eot_id|>\n"
    
    prompt += "<|start_header_id|>assistant<|end_header_id|>\n\n"
    
    if req.response_format and req.response_format.get("type") == "json_object":
        prompt += "```json\n"

    try:
        config = ov_genai.GenerationConfig()
        config.max_new_tokens = req.max_tokens
        config.temperature = req.temperature
        
        # Simple generation
        output = pipe.generate(prompt, config)
        
        # Remove markdown wrappers if json expected
        if req.response_format and req.response_format.get("type") == "json_object":
            if "```" in output:
                output = output.split("```")[0]
            if not output.strip().endswith("}"):
                output += "\n}"
                
        return {
            "id": "chatcmpl-ov",
            "object": "chat.completion",
            "created": 1234567890,
            "model": req.model,
            "choices": [{
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": output
                },
                "finish_reason": "stop"
            }],
            "usage": {
                "prompt_tokens": 0,
                "completion_tokens": 0,
                "total_tokens": 0
            }
        }
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=8000)
