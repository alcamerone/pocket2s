variable "region" {
    type = string
}

variable "stage" {
    type = string
}

provider "aws" {
    region = var.region
}

# Lambdas and associated resources
resource "aws_s3_bucket" "lambdas_bucket" {
    bucket = "pocket2s-lambda-code-bucket"
}

resource "aws_s3_bucket_acl" "lambdas_bucket_acl" {
    bucket = aws_s3_bucket.lambdas_bucket.id
    acl = "private"
}

resource "aws_s3_bucket_versioning" "lambdas_bucket_versioning" {
    bucket = aws_s3_bucket.lambdas_bucket.id
    versioning_configuration {
      status = "Enabled"
    }
}

data "archive_file" "ws_handler_archive" {
    type = "zip"
    source_file = "${path.module}/lambda/ws/bootstrap"
    output_path = "${path.module}/lambda/ws/ws_handler.zip"
}

data "archive_file" "http_handler_archive" {
    type = "zip"
    source_file = "${path.module}/lambda/http/bootstrap"
    output_path = "${path.module}/lambda/http/http_handler.zip"
}

resource "aws_s3_object" "ws_handler_code" {
    bucket = aws_s3_bucket.lambdas_bucket.bucket
    key = "ws_handler.zip"
    source = data.archive_file.ws_handler_archive.output_path
}

resource "aws_s3_object" "http_handler_code" {
    bucket = aws_s3_bucket.lambdas_bucket.bucket
    key = "ws_handler.zip"
    source = data.archive_file.http_handler_archive.output_path
}

data "aws_iam_policy_document" "lambda_execution_assume_role_permissions" {
    statement {
      effect = "Allow"

      principals {
        type = "Service"
        identifiers = ["lambda.amazonaws.com"]
      }

      actions = ["sts:AssumeRole"]
    }
}

resource "aws_iam_role" "http_lambda" {
    name = "Pocket2sHttpLambda"
    assume_role_policy = data.aws_iam_policy_document.lambda_execution_assume_role_permissions.json
}

resource "aws_iam_role" "ws_lambda" {
    name = "Pocket2sWsLambda"
    assume_role_policy = data.aws_iam_policy_document.lambda_execution_assume_role_permissions.json
}

data "aws_iam_policy_document" "rw_room_state_table_permissions" {
    statement {
      effect = "Allow"
      actions = [
        "dynamodb:PutItem",
        "dynamodb:DeleteItem",
        "dynamodb:GetItem"
      ]
      resources = ["arn:aws:dynamodb:eu-west-1:798267245359:table/Pocket2sRoomState"]
    }
}

resource "aws_iam_policy" "rw_room_state_table" {
    name = "ReadWritePocket2sRoomStateTable"
    policy = data.aws_iam_policy_document.rw_room_state_table_permissions.json
}

data "aws_iam_policy_document" "rw_conn_room_player_mapping_table_permissions" {
    statement {
      effect = "Allow"
      actions = [
        "dynamodb:PutItem",
        "dynamodb:DeleteItem",
        "dynamodb:GetItem"
      ]
      resources = ["arn:aws:dynamodb:eu-west-1:798267245359:table/Pocket2s_Conn-Room-Player_Mapping"]
    }
}

resource "aws_iam_policy" "rw_conn_room_player_mapping_table" {
    name = "ReadWritePocket2s_Conn-Room-Player_MappingTable"
    policy = data.aws_iam_policy_document.rw_conn_room_player_mapping_table_permissions.json
}

data "aws_iam_policy_document" "post_to_ws_api_connections_permissions" {
    statement {
      effect = "Allow"
      actions = ["execute-api:ManageConnections"]
      resources = ["${aws_apigatewayv2_api.ws_api.arn}/${var.stage}/POST/@connections/*"]
    }
}

resource "aws_iam_policy" "post_to_ws_api_connections" {
    name = "PostToPocket2sWsApiConnections"
    policy = data.aws_iam_policy_document.post_to_ws_api_connections_permissions.json
}

resource "aws_iam_role_policy_attachment" "http_lambda-rw_room_state_table" {
    role = aws_iam_role.http_lambda.name
    policy_arn = aws_iam_policy.rw_room_state_table.arn
}

resource "aws_iam_role_policy_attachment" "ws_lambda-rw_room_state_table" {
    role = aws_iam_role.ws_lambda.name
    policy_arn = aws_iam_policy.rw_room_state_table.arn
}

resource "aws_iam_role_policy_attachment" "ws_lambda-rw_conn_room_player_mapping_table" {
    role = aws_iam_role.ws_lambda.name
    policy_arn = aws_iam_policy.rw_conn_room_player_mapping_table.arn
}

resource "aws_iam_role_policy_attachment" "ws_lambda-post_to_ws_api_connections" {
    role = aws_iam_role.ws_lambda.name
    policy_arn = aws_iam_policy.post_to_ws_api_connections.arn
}

resource "aws_lambda_function" "http_handler" {
    function_name = "Pocket2sHttpHandler"
    role = aws_iam_role.http_lambda.arn
    s3_bucket = aws_s3_object.http_handler_code.bucket
    s3_key = aws_s3_object.http_handler_code.key
    architectures = ["arm64"]
    runtime = "provided.al2023"
    handler = "bootstrap"
}

resource "aws_lambda_function" "ws_handler" {
    function_name = "Pocket2sWsHandler"
    role = aws_iam_role.ws_lambda.arn
    s3_bucket = aws_s3_object.ws_handler_code.bucket
    s3_key = aws_s3_object.ws_handler_code.key
    architectures = ["arm64"]
    runtime = "provided.al2023"
    handler = "bootstrap"
}

# WS API
resource "aws_apigatewayv2_api" "ws_api" {
    name = "Pocket2sWsApi"
    protocol_type = "WEBSOCKET"
}

resource "aws_apigatewayv2_stage" "ws_api_prod" {
    api_id = aws_apigatewayv2_api.ws_api.id
    name = var.stage
}

resource "aws_apigatewayv2_integration" "ws_integration" {
    api_id = aws_apigatewayv2_api.ws_api.id
    integration_type = "AWS_PROXY"

    integration_uri = aws_lambda_function.ws_handler.invoke_arn
    integration_method = "ANY"
}

resource "aws_apigatewayv2_route" "connect" {
    api_id = aws_apigatewayv2_api.ws_api.id
    route_key = "$connect"

    target = "integrations/${aws_apigatewayv2_integration.ws_integration.id}"
}

resource "aws_apigatewayv2_route" "disconnect" {
    api_id = aws_apigatewayv2_api.ws_api.id
    route_key = "$disconnect"

    target = "integrations/${aws_apigatewayv2_integration.ws_integration.id}"
}

resource "aws_apigatewayv2_route" "default" {
    api_id = aws_apigatewayv2_api.ws_api.id
    route_key = "$default"

    target = "integrations/${aws_apigatewayv2_integration.ws_integration.id}"
}

# HTTP API
resource "aws_apigatewayv2_api" "http_api" {
    name = "Pocket2sHttpApi"
    protocol_type = "HTTP"

    cors_configuration {
      allow_methods = ["OPTIONS", "GET", "POST"]
      allow_headers = ["Content-Type"]
      allow_origins = ["https://pocket2s.com"]
    }
}

resource "aws_apigatewayv2_stage" "http_api_prod" {
    api_id = aws_apigatewayv2_api.http_api.id
    name = "$default"
    auto_deploy = true
}

resource "aws_apigatewayv2_integration" "http_integration" {
    api_id = aws_apigatewayv2_api.ws_api.id
    integration_type = "AWS_PROXY"

    integration_uri = aws_lambda_function.http_handler.invoke_arn
    integration_method = "ANY"
}

resource "aws_apigatewayv2_route" "http_default" {
    api_id = aws_apigatewayv2_api.http_api.id
    route_key = "$default"

    target = "integrations/${aws_apigatewayv2_integration.http_integration.id}"
}

# DynamoDB Tables
resource "aws_dynamodb_table" "room_state" {
    name = "Pocket2sRoomState"
    hash_key = "Id"

    attribute {
      name = "Id"
      type = "S"
    }
}

resource "aws_dynamodb_table" "conn_room_player_mapping" {
    name = "Pocket2s_Conn-Room-Player_Mapping"
    hash_key = "ConnectionId"

    attribute {
      name = "ConnectionId"
      type = "S"
    }
}