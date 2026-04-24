import ast
import hashlib
import json
import os
import subprocess
import textwrap
import time


def build_repo(root: str, files: int = 180, funcs_per_file: int = 16) -> None:
    os.makedirs(root, exist_ok=True)
    for i in range(files):
        lines = ["import json", "import math", "import hashlib"]
        if i:
            lines.append(f"from mod_{i - 1} import func_{i - 1}_0")
        for j in range(funcs_per_file):
            lines.append(
                f"def func_{i}_{j}(x):\n"
                "    total = 0\n"
                "    for k in range(80):\n"
                "        total += (k + 1) * x\n"
                "    return total"
            )
        with open(os.path.join(root, f"mod_{i}.py"), "w", encoding="utf-8") as f:
            f.write("\n\n".join(lines))


def analyze_repo(root: str) -> dict:
    summary = {"files": 0, "funcs": 0, "imports": 0, "calls": 0}
    for name in os.listdir(root):
        path = os.path.join(root, name)
        with open(path, encoding="utf-8") as f:
            tree = ast.parse(f.read(), filename=name)
        summary["files"] += 1
        for node in ast.walk(tree):
            if isinstance(node, ast.FunctionDef):
                summary["funcs"] += 1
            elif isinstance(node, (ast.Import, ast.ImportFrom)):
                summary["imports"] += 1
            elif isinstance(node, ast.Call):
                summary["calls"] += 1
    return summary


def write_and_run_tests(root: str) -> dict:
    calc_path = os.path.join(root, "calc.py")
    test_path = os.path.join(root, "test_calc.py")
    with open(calc_path, "w", encoding="utf-8") as f:
        f.write(
            textwrap.dedent(
                """
                def fib(n):
                    if n < 0:
                        raise ValueError("n must be non-negative")
                    a, b = 0, 1
                    for _ in range(n):
                        a, b = b, a + b
                    return a

                def normalize(nums):
                    total = sum(nums)
                    if total == 0:
                        return [0 for _ in nums]
                    return [round(x / total, 6) for x in nums]
                """
            ).strip()
            + "\n"
        )
    with open(test_path, "w", encoding="utf-8") as f:
        f.write(
            textwrap.dedent(
                """
                import unittest
                from calc import fib, normalize

                class CalcTests(unittest.TestCase):
                    def test_fib(self):
                        self.assertEqual(fib(20), 6765)

                    def test_negative(self):
                        with self.assertRaises(ValueError):
                            fib(-1)

                    def test_normalize(self):
                        self.assertEqual(normalize([2, 3, 5]), [0.2, 0.3, 0.5])

                    def test_zero(self):
                        self.assertEqual(normalize([0, 0]), [0, 0])

                if __name__ == "__main__":
                    unittest.main()
                """
            ).strip()
            + "\n"
        )
    proc = subprocess.run(
        ["python", "-m", "unittest", "-q"],
        cwd=root,
        capture_output=True,
        text=True,
        timeout=20,
        check=False,
    )
    return {
        "returncode": proc.returncode,
        "stdout": proc.stdout,
        "stderr": proc.stderr,
    }


def rank_documents(count: int = 12000) -> dict:
    docs = []
    for i in range(count):
        text = (
            f"doc {i} sandbox execution agent memory policy patch verify "
            f"python testing parser ranking "
        ) * 12 + str(i)
        docs.append(text)

    index = {}
    for idx, doc in enumerate(docs):
        for tok in set(doc.split()):
            index.setdefault(tok, []).append(idx)

    counts = {}
    for tok in "sandbox agent patch verify parser".split():
        for idx in index.get(tok, []):
            counts[idx] = counts.get(idx, 0) + 1

    top = sorted(
        (
            (score, hashlib.md5(docs[i].encode()).hexdigest()[:12], i)
            for i, score in counts.items()
        ),
        reverse=True,
    )[:10]
    return {"docs": len(docs), "tokens": len(index), "matched": len(counts), "top": top}


def main() -> None:
    start = time.time()
    root = "/tmp/heavy_agent_workload"
    build_repo(root)
    repo_summary = analyze_repo(root)
    test_summary = write_and_run_tests(root)
    ranking_summary = rank_documents()

    result = {
        "repo": repo_summary,
        "tests": test_summary,
        "ranking": ranking_summary,
        "elapsed_seconds": round(time.time() - start, 3),
    }
    print(json.dumps(result))


if __name__ == "__main__":
    main()
