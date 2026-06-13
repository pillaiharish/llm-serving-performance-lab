import os

from openai import OpenAI


MODEL_NAME = os.getenv("MODEL_NAME", "models/Qwen/Qwen2.5-0.5B-Instruct")
BASE_URL = os.getenv("BASE_URL", "http://localhost:8000/v1")


def main() -> None:
    client = OpenAI(
        base_url=BASE_URL,
        api_key="not-needed",
    )

    response = client.chat.completions.create(
        model=MODEL_NAME,
        messages=[
            {
                "role": "system",
                "content": "You are a concise assistant for LLM serving experiments.",
            },
            {
                "role": "user",
                "content": "Explain TTFT and ITL in LLM serving in simple terms.",
            },
        ],
        temperature=0,
        max_tokens=128,
    )

    print(response.choices[0].message.content)


if __name__ == "__main__":
    main()