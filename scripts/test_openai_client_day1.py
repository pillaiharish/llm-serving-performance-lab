from openai import OpenAI

MODEL = "Qwen/Qwen2.5-1.5B-Instruct"

client = OpenAI(
    base_url="http://localhost:8000/v1",
    api_key="local-dev",
)

print("Listing models...")
models = client.models.list()
print(models)

print("\nRunning chat completion...")
response = client.chat.completions.create(
    model=MODEL,
    messages=[
        {"role": "system", "content": "You are a concise technical assistant."},
        {"role": "user", "content": "Explain why OpenAI-compatible APIs matter for LLM infrastructure."},
    ],
    temperature=0,
    max_tokens=180,
)

print(response.choices[0].message.content)
