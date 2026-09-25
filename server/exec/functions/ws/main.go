package ws

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/alcamerone/pocket2s/db"
	"github.com/alcamerone/pocket2s/messaging"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func handleMessage(msg json.RawMessage) (any, error) {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	m := messaging.FromPlayerMessage{}
	err := json.Unmarshal(msg, &m)
	if err != nil {
		log.Printf("Error unmarshalling incoming message. Message: '%s', Error: %s", string(msg), err.Error())
		return nil, err
	}

	// Instantiate room store and messenger
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(os.Getenv("AWS_REGION")))
	if err != nil {
		log.Printf("Error initialising AWS context: %s", err.Error())
		return nil, err
	}
	ddbClient := dynamodb.NewFromConfig(cfg)
	roomStore := db.NewDDBRoomStore(ddbClient, os.Getenv("DDB_ROOM_STATE_TABLE"))

	// Get room state
	r, err := roomStore.GetRoom(context.Background(), m.RoomId)
	if err != nil {
		log.Printf("Error retrieving room %s: %s", m.RoomId, err.Error())
		return nil, err
	}

	// Process player action
	r.HandleMessageFromPlayer(m)
	// Broadcast new table state if necessary
	// Persist new room state

	return nil, nil
}

func main() {
	lambda.Start(handleMessage)
}
