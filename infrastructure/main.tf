variable "region" {
    type = string
}

variable "stage" {
    type = string
}

variable "pocket2s_zone_id" {
    type = string
}

provider "aws" {
    region = var.region
}

data "aws_caller_identity" "current" {}

module "dynamodb" {
    source = "./dynamodb"
}

module "lambda" {
  source = "./lambda"
  region = var.region
  stage = var.stage
  http_handler_role_arn = module.iam.http_handler_role_arn
  ws_handler_role_arn = module.iam.ws_handler_role_arn
  room_state_ddb_table_name = module.dynamodb.room_state_table_name
  conn_room_player_mapping_ddb_table_name = module.dynamodb.conn_room_player_mapping_table_name
  ws_api_id = module.apis.ws_api_id
}

module "apis" {
    source = "./apis"
    region = var.region
    stage = var.stage
    pocket2s_zone_id = var.pocket2s_zone_id
    http_handler_lambda_invoke_arn = module.lambda.http_handler_lambda_invoke_arn
    http_handler_lambda_func_name = module.lambda.http_handler_lambda_func_name
    ws_handler_lambda_invoke_arn = module.lambda.ws_handler_lambda_invoke_arn
    ws_handler_lambda_func_name = module.lambda.ws_handler_lambda_func_name
}

module "iam" {
    source = "./iam"
    region = var.region
    stage = var.stage
    room_state_ddb_table_arn = module.dynamodb.room_state_table_arn
    conn_room_player_mapping_ddb_table_arn = module.dynamodb.conn_room_player_mapping_table_arn
    ws_api_execution_arn = module.apis.ws_api_execution_arn
}
