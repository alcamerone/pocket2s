resource "aws_dynamodb_table" "room_state" {
    name = "Pocket2sRoomState"
    billing_mode = "PAY_PER_REQUEST"
    hash_key = "Id"

    attribute {
      name = "Id"
      type = "S"
    }

    ttl {
      enabled = true
      attribute_name = "ExpiresAt"
    }
}

resource "aws_dynamodb_table" "conn_room_player_mapping" {
    name = "Pocket2s_Conn-Room-Player_Mapping"
    billing_mode = "PAY_PER_REQUEST"
    hash_key = "ConnectionId"

    attribute {
      name = "ConnectionId"
      type = "S"
    }

    ttl {
      enabled = true
      attribute_name = "ExpiresAt"
    }
}

output "room_state_table_arn" {
  value = aws_dynamodb_table.room_state.arn
}

output "room_state_table_name" {
  value = aws_dynamodb_table.room_state.name
}

output "conn_room_player_mapping_table_arn" {
  value = aws_dynamodb_table.conn_room_player_mapping.arn
}

output "conn_room_player_mapping_table_name" {
  value = aws_dynamodb_table.conn_room_player_mapping.name
}
