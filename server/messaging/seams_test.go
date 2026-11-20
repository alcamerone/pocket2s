package messaging

import (
	ddbTypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type ddbAV = ddbTypes.AttributeValue

func swapMarshal(f func(any) (map[string]ddbAV, error)) func() {
	orig := marshalMap
	marshalMap = f
	return func() { marshalMap = orig }
}

func swapJSON(f func(any) ([]byte, error)) func() {
	orig := jsonMarshal
	jsonMarshal = f
	return func() { jsonMarshal = orig }
}
