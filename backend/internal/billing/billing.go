package billing

const PAYG = "payg"

type Config struct {
	Model                  string
	ExecutionRateUSDPerSec float64
}
