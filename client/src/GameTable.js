import React, { Component, Fragment } from "react";
import Player from "./Player.js";
import ControlBar from "./ControlBar.js";
import config from "./config.js";
import Deck from "./react-poker/Deck.js";
import "./react-poker/Card.scss";

const MSG_TYPE_HELLO = 1;
const MSG_TYPE_TABLE_STATE = 5;
// TODO
// const MSG_TYPE_PLAYER_CONNECTED = 8;
// const MSG_TYPE_PLAYER_DISCONNECTED = 9;

const intToDollars = (i) => {
	const dollars = i / 100;
	return `$${dollars.toFixed(2)}`;
};

export default class GameTable extends Component {
	constructor(props) {
		super(props);
		this.state = {
			error: null,
			conn: null,
			connected: false,
			table: null,
			player: null,
			result: null
		};
		this.processMessage = this.processMessage.bind(this);
	}

	componentDidMount() {
		const { pathname } = window.location;
		const tableId = pathname.substr(1, pathname.lastIndexOf("/") - 1);
		const playerId = pathname.substr(pathname.lastIndexOf("/") + 1);
		// TODO config
		const conn = new WebSocket(`${config.hostUrl}/connect/${tableId}/${playerId}`);
		conn.onmessage = this.processMessage;
		conn.onerror = (e) => {
			console.log("Error while connecting", e);
			this.setState({ error: e });
		};
		this.setState({ conn });
	}

	processMessage(msg) {
		let data;
		try {
			data = JSON.parse(msg.data);
		} catch (e) {
			console.log(`Error parsing message ${msg.data}: ${e}`);
		}
		console.log("Received message:", data);
		switch (data.Type) {
			case MSG_TYPE_HELLO: {
				this.setState({ connected: true });
				break;
			}
			case MSG_TYPE_TABLE_STATE: {
				this.setState({
					table: data.TableState,
					player: data.PlayerState,
					result: data.Result
				});
				break;
			}
			default: {
				console.log("Received unrecognised message type", data.Type);
			}
		}
	}

	render() {
		if (!this.state.connected) {
			return (
				<div
					style={{
						height: "480px",
						width: "960px",
						margin: "auto",
						textAlign: "center"
					}}
				>
					Please wait, connecting to Pocket2s server...
				</div>
			);
		}
		if (this.state.error) {
			return (
				<div
					style={{
						height: "480px",
						width: "960px",
						margin: "auto",
						textAlign: "center"
					}}
				>
					Sorry, there was an error: {this.state.error}
				</div>
			);
		}
		if (!this.state.table) {
			return (
				<Fragment>
					<div
						style={{
							height: "480px",
							width: "960px",
							margin: "auto",
							textAlign: "center"
						}}
					>
						Waiting for other players...
					</div>
					<ControlBar
						connected={this.state.connected}
						table={this.state.table}
						player={this.state.player}
						conn={this.state.conn}
					/>
				</Fragment>
			);
		}
		return (
			<Fragment>
				<table style={{ height: "480px", width: "960px", margin: "auto" }}>
					<tbody>
						<tr>
							<td style={{ width: "240px", height: "160px" }} />
							<td
								style={{
									width: "240px",
									height: "160px",
									backgroundColor: "purple",
									border: "solid 2px black"
								}}
							>
								<Player
									table={this.state.table}
									seat={0}
									player={this.state.player}
								/>
							</td>
							<td
								style={{
									width: "240px",
									height: "160px",
									backgroundColor: "purple",
									border: "solid 2px black"
								}}
							>
								<Player
									table={this.state.table}
									seat={1}
									player={this.state.player}
								/>
							</td>
							<td style={{ width: "240px", height: "160px" }} />
						</tr>
						<tr>
							<td
								style={{
									width: "240px",
									height: "160px",
									backgroundColor: "purple",
									border: "solid 2px black"
								}}
							>
								<Player
									table={this.state.table}
									seat={2}
									player={this.state.player}
								/>
							</td>
							<td
								colSpan="2"
								style={{
									width: "480px",
									height: "160px",
									textAlign: "center",
									backgroundColor: "darkgreen"
								}}
							>
								<div
									className="deck-container"
									style={{
										position: "relative",
										height: "40px",
										width: "93%",
										margin: "auto",
										marginBottom: "70px"
									}}
								>
									<Deck
										board={
											this.state.table.Cards
												? this.state.table.Cards
												: []
										}
										boardXoffset={80}
										boardYoffset={30}
										size={60}
									/>
								</div>
								<div>
									{this.state.result
										? this.state.result
										: "POT: " + intToDollars(this.state.table.Pot)}
								</div>
							</td>
							<td
								style={{
									width: "240px",
									height: "160px",
									backgroundColor: "purple",
									border: "solid 2px black"
								}}
							>
								<Player
									table={this.state.table}
									seat={3}
									player={this.state.player}
								/>
							</td>
						</tr>
						<tr>
							<td style={{ width: "240px", height: "160px" }} />
							<td
								style={{
									width: "240px",
									height: "160px",
									backgroundColor: "purple",
									border: "solid 2px black"
								}}
							>
								<Player
									table={this.state.table}
									seat={4}
									player={this.state.player}
								/>
							</td>
							<td
								style={{
									width: "240px",
									height: "160px",
									backgroundColor: "purple",
									border: "solid 2px black"
								}}
							>
								<Player
									table={this.state.table}
									seat={5}
									player={this.state.player}
								/>
							</td>
							<td style={{ width: "240px", height: "160px" }} />
						</tr>
					</tbody>
				</table>
				<ControlBar
					connected={this.state.connected}
					table={this.state.table}
					player={this.state.player}
					conn={this.state.conn}
				/>
			</Fragment>
		);
	}
}
