package db

import (
	ddbTypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type ddbAV = ddbTypes.AttributeValue

// swapMarshal replaces the marshalMap seam and returns a function restoring it.
func swapMarshal(f func(any) (map[string]ddbAV, error)) func() {
	orig := marshalMap
	marshalMap = f
	return func() { marshalMap = orig }
}
