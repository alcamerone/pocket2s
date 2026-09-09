module github.com/alcamerone/pocket2s

go 1.26

require (
	github.com/alcamerone/joker v0.0.1
	github.com/aws/aws-lambda-go v1.55.0
	github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue v1.21.3
	github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi v1.36.0
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.67.0
	github.com/gocraft/web v0.0.0-20190207150652-9707327fb69b
	github.com/gorilla/websocket v1.5.3
)

replace github.com/alcamerone/joker v0.0.1 => ../joker

require (
	github.com/aws/aws-sdk-go-v2 v1.46.0 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/dynamodbstreams v1.40.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.13.2 // indirect
	github.com/aws/smithy-go v1.28.1 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
)
