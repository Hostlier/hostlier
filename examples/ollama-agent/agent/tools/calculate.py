"""Bounded arithmetic without executing model-authored code."""

import math

from harnest.agent import tool


@tool
def calculate(left: float, operator: str, right: float) -> dict:
    """Calculate two finite numbers. left is the first operand, right the second;
    operator must be add, subtract, multiply, or divide.
    """
    if not math.isfinite(left) or not math.isfinite(right):
        return {"error": "Operands must be finite numbers"}
    if operator == "add":
        value = left + right
    elif operator == "subtract":
        value = left - right
    elif operator == "multiply":
        value = left * right
    elif operator == "divide" and right != 0:
        value = left / right
    else:
        return {"error": "Unsupported operation or division by zero"}
    if not math.isfinite(value):
        return {"error": "Result exceeds numeric range"}
    return {"result": value}
