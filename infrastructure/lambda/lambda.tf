variable "region" {
    type = string
}

variable "stage" {
    type = string
}

variable "http_handler_role_arn" {
    type = string
}

variable "ws_handler_role_arn" {
    type = string
}

variable "room_state_ddb_table_name" {
    type = string
}

variable "conn_room_player_mapping_ddb_table_name" {
    type = string
}

variable "ws_api_id" {
    type = string
}

resource "aws_s3_bucket" "lambdas_bucket" {
    bucket = "pocket2s-lambda-code-bucket"
}

resource "aws_s3_bucket_versioning" "lambdas_bucket_versioning" {
    bucket = aws_s3_bucket.lambdas_bucket.id
    versioning_configuration {
      status = "Enabled"
    }
}

data "archive_file" "ws_handler" {
    type = "zip"
    source_file = "${path.module}/ws/bootstrap"
    output_path = "${path.module}/ws/ws_handler.zip"
}

data "archive_file" "http_handler" {
    type = "zip"
    source_file = "${path.module}/http/bootstrap"
    output_path = "${path.module}/http/http_handler.zip"
}

resource "aws_s3_object" "ws_handler_code" {
    bucket = aws_s3_bucket.lambdas_bucket.bucket
    key = replace(
        basename("ws_handler.zip"),
        ".zip",
        "-${data.archive_file.ws_handler.output_md5}.zip")
    source = data.archive_file.ws_handler.output_path
}

resource "aws_s3_object" "http_handler_code" {
    bucket = aws_s3_bucket.lambdas_bucket.bucket
    key = replace(
        basename("http_handler.zip"),
        ".zip",
        "-${data.archive_file.http_handler.output_md5}.zip")
    source = data.archive_file.http_handler.output_path
}

resource "aws_lambda_function" "http_handler" {
    function_name = "Pocket2sHttpHandler"
    role = var.ws_handler_role_arn
    s3_bucket = aws_s3_object.http_handler_code.bucket
    s3_key = aws_s3_object.http_handler_code.key
    architectures = ["arm64"]
    runtime = "provided.al2023"
    handler = "bootstrap"

    reserved_concurrent_executions = 10

    environment {
      variables = {
        "DDB_ROOM_STATE_TABLE" = var.room_state_ddb_table_name
      }
    }
}

resource "aws_lambda_function" "ws_handler" {
    function_name = "Pocket2sWsHandler"
    role = var.ws_handler_role_arn
    s3_bucket = aws_s3_object.ws_handler_code.bucket
    s3_key = aws_s3_object.ws_handler_code.key
    architectures = ["arm64"]
    runtime = "provided.al2023"
    handler = "bootstrap"

    reserved_concurrent_executions = 10

     environment {
      variables = {
        "DDB_ROOM_STATE_TABLE" = var.room_state_ddb_table_name
        "DDB_CONN_TO_ROOM_PLAYER_TABLE" = var.conn_room_player_mapping_ddb_table_name
        "AWS_APIGW_WS_ENDPOINT" = "https://${var.ws_api_id}.execute-api.${var.region}.amazonaws.com/${var.stage}"
      }
    }
}

output "http_handler_lambda_invoke_arn" {
    value = aws_lambda_function.http_handler.invoke_arn
}

output "http_handler_lambda_func_name" {
    value = aws_lambda_function.http_handler.function_name
}

output "ws_handler_lambda_invoke_arn" {
    value = aws_lambda_function.ws_handler.invoke_arn
}

output "ws_handler_lambda_func_name" {
    value = aws_lambda_function.ws_handler.function_name
}
