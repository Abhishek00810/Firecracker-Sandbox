package billing

const PAYG = "payg"

type Config struct {
	Model string
	// ExecutionRateUSDPerSec matches the system-wide unit: the DB column
	// (pricing_rates.rate_execution_sec) and the sk platform both bill
	// per-second; per-hour figures are display-only conversions in sk.
	ExecutionRateUSDPerSec float64
}
