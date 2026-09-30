package perfsample

import "math"

// approxEq 是全库统一的浮点权重比较口径。
func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
