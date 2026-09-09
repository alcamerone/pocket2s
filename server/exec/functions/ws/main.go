package ws

import (
	"encoding/json"

	"github.com/aws/aws-lambda-go/lambda"
)

func handleMessage(msg json.RawMessage) (any, error) {
	// Instantiate room and connection stores
	// Get room state
	// Retrieve relevant connections
	// Get table state
	// Process player action

	return nil, nil
}

func main() {
	lambda.Start(handleMessage)
}
