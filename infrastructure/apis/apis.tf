variable "region" {
    type = string
}

variable "stage" {
    type = string
}

variable "pocket2s_zone_id" {
    type = string
}

variable "http_handler_lambda_invoke_arn" {
    type = string
}

variable "http_handler_lambda_func_name" {
    type = string
}

variable "ws_handler_lambda_invoke_arn" {
  type = string
}

variable "ws_handler_lambda_func_name" {
    type = string
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

    default_route_settings {
      throttling_rate_limit = 20 # req/s
      throttling_burst_limit = 40
    }
}

resource "aws_apigatewayv2_integration" "http_integration" {
    api_id = aws_apigatewayv2_api.http_api.id
    integration_type = "AWS_PROXY"
    integration_uri = var.http_handler_lambda_invoke_arn
    integration_method = "POST"
    payload_format_version = "2.0"
}

resource "aws_acm_certificate" "http_api" {
    domain_name = "api.pocket2s.com"
    validation_method = "DNS"

    lifecycle {
      create_before_destroy = true
    }
}

resource "aws_apigatewayv2_domain_name" "http_api" {
    domain_name = "api.pocket2s.com"
    routing_mode = "API_MAPPING_ONLY"

    domain_name_configuration {
      certificate_arn = aws_acm_certificate.http_api.arn
      endpoint_type = "REGIONAL"
      security_policy = "TLS_1_2"
    }
}

resource "aws_apigatewayv2_api_mapping" "http_api" {
    api_id = aws_apigatewayv2_api.http_api.id
    domain_name = aws_apigatewayv2_domain_name.http_api.id
    stage = aws_apigatewayv2_stage.http_api_prod.id
}

resource "aws_route53_record" "http_api" {
    name = aws_apigatewayv2_domain_name.http_api.domain_name
    type = "A"
    zone_id = var.pocket2s_zone_id

    alias {
      name = aws_apigatewayv2_domain_name.http_api.domain_name_configuration[0].target_domain_name
      zone_id = aws_apigatewayv2_domain_name.http_api.domain_name_configuration[0].hosted_zone_id
      evaluate_target_health = false
    }
}

resource "aws_apigatewayv2_route" "http_get" {
    api_id = aws_apigatewayv2_api.http_api.id
    route_key = "GET /{proxy+}"

    target = "integrations/${aws_apigatewayv2_integration.http_integration.id}"
}

resource "aws_apigatewayv2_route" "http_post" {
    api_id = aws_apigatewayv2_api.http_api.id
    route_key = "POST /{proxy+}"

    target = "integrations/${aws_apigatewayv2_integration.http_integration.id}"
}

resource "aws_lambda_permission" "http_api_invoke" {
    statement_id = "AllowPocket2sHttpApiInvocation"
    action = "lambda:InvokeFunction"
    function_name = var.http_handler_lambda_func_name
    principal = "apigateway.amazonaws.com"
    source_arn = "${aws_apigatewayv2_api.http_api.execution_arn}/*/*"
}

# WS API
resource "aws_apigatewayv2_api" "ws_api" {
    name = "Pocket2sWsApi"
    protocol_type = "WEBSOCKET"
    route_selection_expression = "$request.body.action"
}

resource "aws_apigatewayv2_stage" "ws_api_prod" {
    api_id = aws_apigatewayv2_api.ws_api.id
    name = var.stage
    auto_deploy = true

    default_route_settings {
      throttling_rate_limit = 20 # req/s
      throttling_burst_limit = 40
    }
}

resource "aws_apigatewayv2_integration" "ws_integration" {
    api_id = aws_apigatewayv2_api.ws_api.id
    integration_type = "AWS_PROXY"

    integration_uri = var.ws_handler_lambda_invoke_arn
    integration_method = "POST"
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

resource "aws_acm_certificate" "ws_api" {
    domain_name = "ws.pocket2s.com"
    validation_method = "DNS"

    lifecycle {
      create_before_destroy = true
    }
}

resource "aws_apigatewayv2_domain_name" "ws_api" {
    domain_name = "ws.pocket2s.com"
    routing_mode = "API_MAPPING_ONLY"

    domain_name_configuration {
      certificate_arn = aws_acm_certificate.ws_api.arn
      endpoint_type = "REGIONAL"
      security_policy = "TLS_1_2"
    }
}

resource "aws_apigatewayv2_api_mapping" "ws_api" {
    api_id = aws_apigatewayv2_api.ws_api.id
    domain_name = aws_apigatewayv2_domain_name.ws_api.id
    stage = aws_apigatewayv2_stage.ws_api_prod.id
}

resource "aws_route53_record" "ws_api" {
    name = aws_apigatewayv2_domain_name.ws_api.domain_name
    type = "A"
    zone_id = var.pocket2s_zone_id

    alias {
      name = aws_apigatewayv2_domain_name.ws_api.domain_name_configuration[0].target_domain_name
      zone_id = aws_apigatewayv2_domain_name.ws_api.domain_name_configuration[0].hosted_zone_id
      evaluate_target_health = false
    }
}

resource "aws_lambda_permission" "ws_api_invoke" {
    statement_id = "AllowPocket2sWsApiInvocation"
    action = "lambda:InvokeFunction"
    function_name = var.ws_handler_lambda_func_name
    principal = "apigateway.amazonaws.com"
    source_arn = "${aws_apigatewayv2_api.ws_api.execution_arn}/*/*"
}

output "ws_api_execution_arn" {
    value = aws_apigatewayv2_api.ws_api.execution_arn
}

output "ws_api_id" {
    value = aws_apigatewayv2_api.ws_api.id
}