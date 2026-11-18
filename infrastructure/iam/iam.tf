variable "region" {
    type = string
}

variable "stage" {
    type = string
}

variable "room_state_ddb_table_arn" {
    type = string
}

variable "conn_room_player_mapping_ddb_table_arn" {
    type = string
}

variable "ws_api_execution_arn" {
    type = string
}

data "aws_caller_identity" "current" {}

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
      resources = [var.room_state_ddb_table_arn]
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
      resources = [var.conn_room_player_mapping_ddb_table_arn]
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
      resources = [
        "${var.ws_api_execution_arn}/${var.stage}/GET/@connections/*",
        "${var.ws_api_execution_arn}/${var.stage}/POST/@connections/*"
      ]
    }
}

resource "aws_iam_policy" "post_to_ws_api_connections" {
    name = "PostToPocket2sWsApiConnections"
    policy = data.aws_iam_policy_document.post_to_ws_api_connections_permissions.json
}

data "aws_iam_policy_document" "create_log_groups_permissions" {
    statement {
      effect = "Allow"
      actions = ["logs:CreateLogGroup"]
      resources = ["arn:aws:logs:${var.region}:${data.aws_caller_identity.current.account_id}:*"]
    }
}

resource "aws_iam_policy" "create_log_groups" {
    name = "CreateLogGroups"
    policy = data.aws_iam_policy_document.create_log_groups_permissions.json
}

data "aws_iam_policy_document" "write_http_handler_logs_permissions" {
    statement {
      effect = "Allow"
      actions = [
        "logs:CreateLogStream",
        "logs:PutLogEvents"
        ]
        resources = ["arn:aws:logs:${var.region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/lambda/Pocket2sHttpHandler:*"]
    }
}

resource "aws_iam_policy" "write_http_handler_logs" {
    name = "WritePocket2sHttpHandlerLogs"
    policy = data.aws_iam_policy_document.write_http_handler_logs_permissions.json
}

data "aws_iam_policy_document" "write_ws_handler_logs_permissions" {
    statement {
      effect = "Allow"
      actions = [
        "logs:CreateLogStream",
        "logs:PutLogEvents"
      ]
      resources = ["arn:aws:logs:${var.region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/lambda/Pocket2sWsHandler:*"]
    }
}

resource "aws_iam_policy" "write_ws_handler_logs" {
    name = "WritePocket2sWsHandlerLogs"
    policy = data.aws_iam_policy_document.write_ws_handler_logs_permissions.json
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

resource "aws_iam_role_policy_attachment" "http_lambda-create_log_groups" {
    role = aws_iam_role.http_lambda.name
    policy_arn = aws_iam_policy.create_log_groups.arn
}

resource "aws_iam_role_policy_attachment" "ws_lambda-create_log_groups" {
    role = aws_iam_role.ws_lambda.name
    policy_arn = aws_iam_policy.create_log_groups.arn
}

resource "aws_iam_role_policy_attachment" "http_lambda-write_http_handler_logs" {
    role = aws_iam_role.http_lambda.name
    policy_arn = aws_iam_policy.write_http_handler_logs.arn
}

resource "aws_iam_role_policy_attachment" "ws_lambda-write_ws_handler_logs" {
    role = aws_iam_role.ws_lambda.name
    policy_arn = aws_iam_policy.write_ws_handler_logs.arn
}

output "http_handler_role_arn" {
    value = aws_iam_role.http_lambda.arn
}

output "ws_handler_role_arn" {
    value = aws_iam_role.ws_lambda.arn
}