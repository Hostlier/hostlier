"""A managed Harnest agent backed by real Ollama Cloud inference."""

import os

from harnest.agent import Agent
from harnest.model import LiteLLMModel


root_agent = Agent(
    name="ollama_assistant",
    description="A small Ollama Cloud assistant with arithmetic and UTC clock tools.",
    model=LiteLLMModel(
        "openai/" + os.getenv("OLLAMA_MODEL", "deepseek-v4.1-flash:1017"),
        api_base="https://ollama.com/v1/",
        api_key=os.getenv("OLLAMA_API_KEY", "not-configured"),
        max_tokens=1024,
        timeout=90,
        num_retries=0,
    ),
    history="turn",
)
