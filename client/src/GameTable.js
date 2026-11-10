import React, { Component, Fragment } from "react";
import Player from "./Player.js";
import ControlBar from "./ControlBar.js";
import config from "./config.js";
import Deck from "./react-poker/Deck.js";
import "./react-poker/Card.scss";

const MSG_TYPE_HELLO = 1;
const MSG_TYPE_TABLE_STATE = 5;
const MSG_TYPE_PLAYER_ACTION = 6;
const MSG_TYPE_ILLEGAL_ACTION = 7;
const MSG_TYPE_PLAYER_CONNECTED = 8;
const MSG_TYPE_PLAYER_DISCONNECTED = 9;
const MSG_TYPE_PING = 10;

const RECONNECT_DELAY_MS = 2000;
// API Gateway closes connections that have been idle for 10 minutes
const PING_INTERVAL_MS = 4 * 60 * 1000;

const intToDollars = (i) => {
	const dollars = i / 100;
	return `$${dollars.toFixed(2)}`;
};

const parseTableAndPlayerIds = () => {
	const { pathname } = window.location;
	const lastSlashIdx = pathname.lastIndexOf("/");
	return {
		tableId: pathname.substring(1, lastSlashIdx),
		playerId: pathname.substring(lastSlashIdx + 1)
	};
};

export default class GameTable extends Component {
	constructor(props) {
		super(props);
		const { tableId, playerId } = parseTableAndPlayerIds();
		this.state = {
			error: null,
			conn: null,
			connected: false,
			connectionCount: 0,
			tableId,
			playerId,
			table: null,
			player: null,
			result: null
		};
		this.unmounted = false;
		this.processMessage = this.processMessage.bind(this);
		this.connect = this.connect.bind(this);
	}

	componentDidMount() {
		this.connect();
	}

	componentWillUnmount() {
		this.unmounted = true;
		clearTimeout(this.reconnectTimer);
		clearInterval(this.pingTimer);
		if (this.state.conn) {
			this.state.conn.close();
		}
	}

	connect() {
		const { tableId, playerId } = this.state;
		// TODO config
		const conn = new WebSocket(`${config.hostUrl}/connect/${tableId}/${playerId}`);
		conn.onopen = () => {
			this.setState({ error: null });
			conn.send(JSON.stringify({ Type: MSG_TYPE_HELLO, RoomId: tableId, PlayerId: playerId }));
			clearInterval(this.pingTimer);
			this.pingTimer = setInterval(() => {
				conn.send(JSON.stringify({ Type: MSG_TYPE_PING, RoomId: tableId, PlayerId: playerId }));
			}, PING_INTERVAL_MS);
		};
		conn.onmessage = this.processMessage;
		conn.onerror = (e) => {
			console.log("Error on connection", e);
			this.setState({ error: "There was a problem with the connection to the server." });
		};
		conn.onclose = () => {
			clearInterval(this.pingTimer);
			if (this.unmounted) {
				return;
			}
			this.setState({ connected: false });
			this.reconnectTimer = setTimeout(this.connect, RECONNECT_DELAY_MS);
		};
		this.setState({ conn });
	}

	processMessage(msg) {
		let data;
		try {
			data = JSON.parse(msg.data);
		} catch (e) {
			console.log(`Error parsing message ${msg.data}: ${e}`);
			return;
		}
		console.log("Received message:", data);
		switch (data.Type) {
			case MSG_TYPE_HELLO: {
				const gameInProgress =
					data.TableState && data.TableState.Seats && data.TableState.Seats.length > 0;
				this.setState((state) => ({
					connected: true,
					connectionCount: state.connectionCount + 1,
					table: gameInProgress ? data.TableState : null,
					player: data.PlayerState
				}));
				break;
			}
			case MSG_TYPE_TABLE_STATE:
			case MSG_TYPE_ILLEGAL_ACTION: {
				this.setState({
					table: data.TableState,
					player: data.PlayerState,
					result: data.Result
				});
				break;
			}
			case MSG_TYPE_PLAYER_ACTION:
			case MSG_TYPE_PLAYER_CONNECTED:
			case MSG_TYPE_PLAYER_DISCONNECTED: {
				// TODO show these to the player
				break;
			}
			default: {
				console.log("Received unrecognised message type", data.Type);
			}
		}
	}

	render() {
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
					Sorry, there was an error: {this.state.error} Retrying...
				</div>
			);
		}
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
						key={this.state.connectionCount}
						connected={this.state.connected}
						tableId={this.state.tableId}
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
					key={this.state.connectionCount}
					connected={this.state.connected}
					tableId={this.state.tableId}
					table={this.state.table}
					player={this.state.player}
					conn={this.state.conn}
				/>
			</Fragment>
		);
	}
}
