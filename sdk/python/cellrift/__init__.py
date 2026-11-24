"""Cellrift Python SDK — thin wrapper around the cellrift CLI binary."""

__version__ = "0.1.0"

import json
import shutil
import subprocess
from typing import Any, Dict, List, Optional


def _find_binary() -> str:
    """Locate the cellrift binary on PATH."""
    path = shutil.which("cellrift")
    if path is None:
        raise FileNotFoundError(
            "cellrift binary not found on PATH. "
            "Install it via: go install github.com/riftcell/cellrift/cmd/cellrift@latest"
        )
    return path


def run(
    scenario: str,
    *,
    target_host: Optional[str] = None,
    variables: Optional[Dict[str, str]] = None,
    format: str = "json",
    fail_fast: bool = False,
    timeout_ms: int = 3000,
) -> Dict[str, Any]:
    """Run a scenario file and return the parsed result.

    Args:
        scenario: Path to a scenario YAML file.
        target_host: Override target host for all steps.
        variables: Dict of variable overrides.
        format: Output format (json recommended for programmatic use).
        fail_fast: Stop on first assertion failure.
        timeout_ms: Global step timeout in milliseconds.

    Returns:
        Parsed JSON result dict when format="json".

    Raises:
        FileNotFoundError: If the cellrift binary is not on PATH.
        subprocess.CalledProcessError: If cellrift exits with code 2 (execution error).
    """
    binary = _find_binary()
    cmd: List[str] = [binary, "run", "--format", format, "--timeout", str(timeout_ms)]

    if target_host:
        cmd.extend(["--target-host", target_host])
    if fail_fast:
        cmd.append("--fail-fast")
    if variables:
        for k, v in variables.items():
            cmd.extend(["--var", f"{k}={v}"])

    cmd.append(scenario)

    result = subprocess.run(cmd, capture_output=True, text=True)

    if result.returncode == 2:
        raise subprocess.CalledProcessError(
            result.returncode, cmd, result.stdout, result.stderr
        )

    if format == "json" and result.stdout.strip():
        return json.loads(result.stdout)

    return {"stdout": result.stdout, "stderr": result.stderr, "returncode": result.returncode}


def validate(scenario: str) -> bool:
    """Validate a scenario file without executing it.

    Returns True if valid, False otherwise.
    """
    binary = _find_binary()
    result = subprocess.run(
        [binary, "validate", scenario], capture_output=True, text=True
    )
    return result.returncode == 0
