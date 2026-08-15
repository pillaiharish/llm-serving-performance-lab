import importlib
import platform
import subprocess
import sys
import os

print(f"Executable: {sys.executable}")
print(f"Working directory: {os.getcwd()}")


def check_import(package_name: str) -> None:
    try:
        module = importlib.import_module(package_name)
        version = getattr(module, "__version__", "unknown")
        print(f"[OK] {package_name}: {version}")
    except Exception as exc:
        print(f"[FAIL] {package_name}: {exc}")


def run_command(command: list[str]) -> None:
    print(f"\n$ {' '.join(command)}")
    try:
        result = subprocess.run(
            command,
            check=False,
            text=True,
            capture_output=True,
        )
        if result.stdout.strip():
            print(result.stdout.strip())
        if result.stderr.strip():
            print(result.stderr.strip())
    except FileNotFoundError:
        print(f"[FAIL] command not found: {command[0]}")


def main() -> None:
    print("=== System ===")
    print(f"Python: {sys.version}")
    print(f"Platform: {platform.platform()}")

    print("\n=== NVIDIA ===")
    run_command(["nvidia-smi"])

    print("\n=== Python packages ===")
    for package in ["torch", "vllm", "openai", "pandas", "matplotlib"]:
        check_import(package)

    print("\n=== CUDA via PyTorch ===")
    try:
        import torch

        print(f"torch.cuda.is_available(): {torch.cuda.is_available()}")
        print(f"torch.version.cuda: {torch.version.cuda}")

        if torch.cuda.is_available():
            print(f"GPU count: {torch.cuda.device_count()}")
            print(f"GPU name: {torch.cuda.get_device_name(0)}")
            print(f"Capability: {torch.cuda.get_device_capability(0)}")
            print(f"Arch list: {torch.cuda.get_arch_list()}")
    except Exception as exc:
        print(f"[FAIL] CUDA check failed: {exc}")


if __name__ == "__main__":
    main()