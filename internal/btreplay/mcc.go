// mcc.go 参数扫参的多重检验校正（§WS-H C2）：扫参本质是数万次并行假设检验——
// 纯凭运气也会有组合显著为正。为排名冠军附带统计显著性，输出单组合 t 检验
// p 值 + Bonferroni 校正后 p 值（校正因子=该战法本次测试的组合数），供审批端
// 判断"冠军是真实超额还是多重比较的幸存者偏差"。
//
// English: multiple-comparison correction for the parameter sweep (WS-H C2). A sweep runs tens of
// thousands of parallel hypothesis tests, so some combos will look significant by luck alone. For
// each strategy champion we emit a one-sample t-test p-value on per-trade net returns plus the
// Bonferroni-corrected p (factor = number of combos tested), letting approval judge whether the win
// is real alpha or multiple-comparison survivor bias.
package btreplay

import "math"

// oneSampleTP 单样本 t 检验的双尾 p 值（H0：逐笔净收益均值 = 0）。
// 用 Student t 分布精确 CDF（正则化不完全 Beta，续分式）计算，样本数 ≥ 2 才有效；
// 方差为 0（全部同收益）返回 1。t = mean / (sd/√n)，df = n-1。
// English: two-sided one-sample t-test p-value (H0: mean per-trade net return = 0), computed with the
// exact Student-t CDF via the regularized incomplete beta continued fraction. Returns 1 when there
// are fewer than 2 trades or zero variance.
func oneSampleTP(returns []float64) float64 {
	n := len(returns)
	if n < 2 {
		return 1
	}
	var sum, sum2 float64
	for _, r := range returns {
		sum += r
		sum2 += r * r
	}
	mean := sum / float64(n)
	variance := sum2/float64(n) - mean*mean
	if variance <= 0 {
		return 1
	}
	sd := math.Sqrt(variance)
	if sd <= 0 {
		return 1
	}
	df := float64(n - 1)
	t := mean / (sd / math.Sqrt(float64(n)))
	// 双尾 p = I_{df/(df+t²)}(df/2, 0.5)（Student t 分布与不完全 Beta 的标准恒等式；
	// x 由 t² 决定，对负 t 对称，故无需区分符号）。
	x := df / (df + t*t)
	p := betainc(x, df/2, 0.5)
	if p > 1 {
		p = 1
	}
	if p < 0 {
		p = 0
	}
	return p
}

// bonferroniP Bonferroni 校正：p_adj = min(1, p × nTests)；nTests ≤ 1 不校正。
// English: Bonferroni correction: p_adj = min(1, p × nTests); no correction when nTests ≤ 1.
func bonferroniP(p float64, nTests int) float64 {
	if nTests <= 1 {
		if p > 1 {
			return 1
		}
		return p
	}
	adj := p * float64(nTests)
	if adj > 1 {
		return 1
	}
	return adj
}

// betainc 正则化不完全 Beta 函数 I_x(a,b)。0≤x≤1，a,b>0。
// 采用标准公式：I_x(a,b) = [x^a(1-x)^b/(a·B(a,b))] × betacf(a,b,x)；
// 当 x ≥ (a+1)/(a+b+2) 时改用对称 I_x(a,b) = 1 - I_{1-x}(b,a)，保证续分式收敛。
// English: regularized incomplete beta I_x(a,b) via the standard continued-fraction formula, with the
// symmetry switch for x near 1 to keep the continued fraction convergent.
func betainc(x, a, b float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	// bt(pa,pb,xa) = xa^pa·(1-xa)^pb / B(pa,pb)
	bt := func(pa, pb, xa float64) float64 {
		if xa == 0 || xa == 1 {
			return 0
		}
		ln := lgamma(pa+pb) - lgamma(pa) - lgamma(pb) + pa*math.Log(xa) + pb*math.Log1p(-xa)
		return math.Exp(ln)
	}
	if x < (a+1)/(a+b+2) {
		return bt(a, b, x) / a * betacf(a, b, x)
	}
	// 对称：I_x(a,b) = 1 - I_{1-x}(b,a)（指数随角色对换）
	return 1 - bt(b, a, 1-x)/b*betacf(b, a, 1-x)
}

// betacf 不完全 Beta 续分式（NumRec 算法）。English: continued-fraction core of betainc.
func betacf(a, b, x float64) float64 {
	const maxIter = 300
	const eps = 3e-12
	const tiny = 1e-300

	qab := a + b
	qap := a + 1
	qam := a - 1

	c := 1.0
	d := 1 - qab*x/qap
	if math.Abs(d) < tiny {
		d = tiny
	}
	d = 1 / d
	h := d

	for i := 1; i <= maxIter; i++ {
		m := float64(i)
		m2 := 2 * m
		aa := m * (b - m) * x / ((qam + m2) * (a + m2))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		h *= d * c

		aa = -(a + m) * (qab + m) * x / ((a + m2) * (qap + m2))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < eps {
			break
		}
	}
	return h
}

// lgamma 对数伽玛函数（委托 math.Lgamma，避免名称冲突）。
func lgamma(x float64) float64 {
	l, _ := math.Lgamma(x)
	return l
}
