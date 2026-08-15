import os
import sys

from openai import OpenAI


MODEL_NAME = os.getenv("MODEL_NAME", "models/Qwen/Qwen2.5-0.5B-Instruct")
BASE_URL = os.getenv("BASE_URL", "http://localhost:8000/v1")


def main() -> None:
    client = OpenAI(
        base_url=BASE_URL,
        api_key="not-needed",
    )

    print(f"Base URL: {BASE_URL}")
    print(f"Model: {MODEL_NAME}")

    models = client.models.list()
    print("Available models:")
    for model in models.data:
        print(f"- {model.id}")

    response = client.chat.completions.create(
        model=MODEL_NAME,
        messages=[
            {
                "role": "user",
                "content": "Reply with exactly: SMOKE_TEST_OK",
            }
        ],
        temperature=0,
        max_tokens=16,
    )

    content = response.choices[0].message.content.strip()
    print(f"Response: {content}")

    if "SMOKE_TEST_OK" not in content:
        print("Smoke test reached server, but response did not match expected text.")
        sys.exit(2)


if __name__ == "__main__":
    main()