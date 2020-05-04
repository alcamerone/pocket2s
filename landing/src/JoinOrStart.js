import React, { Component, Fragment } from "react";
import config from "./config.js";

const nameRe = /[A-Za-z0-9-_]*/;
const valueRe = /\d+(?:\.\d{0,2})/;
const STATE_VAR_ROOMNAME = "roomName";
const STATE_VAR_CREATEROOMNAME = "createRoomName";
const STATE_VAR_PLAYERNAME = "playerName";
const STATE_VAR_CREATEPLAYERNAME = "createPlayerName";
const STATE_VAR_BUYIN = "buyIn";
const STATE_VAR_BIGBLIND = "bigBlind";
const STATE_VAR_SMALLBLIND = "smallBlind";
const STATE_VAR_ANTE = "ante";

const getValidationRe = (stateVar) => {
	switch (stateVar) {
		case STATE_VAR_ROOMNAME:
		case STATE_VAR_CREATEROOMNAME:
		case STATE_VAR_PLAYERNAME:
		case STATE_VAR_CREATEPLAYERNAME:
			return nameRe;
		default:
			return valueRe;
	}
};

export default class JoinOrStart extends Component {
	constructor(props) {
		super(props);
		this.state = {
			showCreateGameDiv: false,
			errorCreateRoom: "",
			roomName: "",
			errorRoomName: "",
			playerName: "",
			errorPlayerName: "",
			createRoomName: "",
			errorCreateRoomName: "",
			createRoomPlayerName: "",
			errorCreateRoomPlayerName: "",
			buyIn: "20.00",
			bigBlind: "0.20",
			smallBlind: "0.10",
			ante: "0.00"
		};
	}

	handleInput(input, stateVar) {
		const re = getValidationRe(stateVar);
		const match = input.match(re);
		if (match.length === 0) {
			return;
		}
		this.setState({ [stateVar]: match[0] });
	}

	joinRoom() {
		if (!this.state.playerName || !this.state.roomName) {
			const errorPlayerName = !this.state.playerName ? "You need a name!" : "";
			const errorRoomName = !this.state.roomName
				? "Let us know what room you want to join!"
				: "";
			this.setState({ errorPlayerName, errorRoomName });
			return;
		}
		window.open(
			`${config.appUrl}/${this.state.roomName}/${this.state.playerName}`,
			"width=1024,height=768"
		);
	}

	createRoom() {
		if (!this.state.createRoomPlayerName || !this.state.createRoomName) {
			const errorCreateRoomPlayerName = !this.state.createRoomPlayerName
				? "You need a name!"
				: "";
			const errorCreateRoomName = !this.state.createRoomName
				? "Your room needs a name!"
				: "";
			this.setState({ errorCreateRoomPlayerName, errorCreateRoomName });
			return;
		}
		fetch(`${config.apiUrl}/create/${this.state.createRoomName}`, {
			method: "POST",
			mode: "cors",
			headers: {
				"Content-Type": "application/json"
			},
			referrerPolicy: "origin",
			body: JSON.stringify({
				BuyIn: this.state.buyIn,
				BigBlind: this.state.bigBlind,
				SmallBlind: this.state.smallBlind,
				Ante: this.state.ante
			})
		})
			.then((resp) => {
				if (resp.ok) {
					window.open(
						`https://app.pocket2s.com/${this.state.createRoomName}/${this.state.createRoomPlayerName}`,
						"width=1024,height=768"
					);
					return;
				}
				if (resp.status === 409) {
					this.setState({
						errorCreateRoomName: "Sorry, that room name is taken!"
					});
					return;
				}
				this.setState({
					errorCreateRoom:
						"Sorry, something went wrong creating your room. Please try again."
				});
			})
			.catch((e) => {
				console.log("Error creating room:", e);
			});
	}

	render() {
		return (
			<div>
				<h3>
					Just enter your player name and the name of the room you want to join
					and click "Join"
				</h3>
				<div className="input-block">
					<div className="input-group" style={{ display: "inline-block" }}>
						<label for="player-name">Player Name</label>
						<input
							id="player-name"
							value={this.state.playerName}
							onChange={(e) => {
								this.handleInput(e.target.value, STATE_VAR_PLAYERNAME);
							}}
						/>
						<div>{this.state.errorPlayerName}</div>
					</div>
					<div className="input-group" style={{ display: "inline-block" }}>
						<label for="room-name">Room Name</label>
						<input
							id="room-name"
							value={this.state.roomName}
							onChange={(e) => {
								this.handleInput(e.target.value, STATE_VAR_ROOMNAME);
							}}
						/>
						<div>{this.state.errorRoomName}</div>
					</div>
					<button
						onClick={() => {
							this.joinRoom();
						}}
					>
						Join
					</button>
				</div>
				<div className="divider" />
				<span
					onClick={() =>
						this.setState({
							showCreateGameDiv: !this.state.showCreateGameDiv
						})
					}
				>
					Or, click here to start a room...
				</span>
				{this.state.showCreateGameDiv && (
					<Fragment>
						<div className="input-block smaller">
							<div className="input-group">
								<label for="create-room-name">Room Name</label>
								<input
									id="create-room-name"
									value={this.state.createRoomName}
									onChange={(e) =>
										this.handleInput(
											e.target.value,
											STATE_VAR_CREATEROOMNAME
										)
									}
								/>
								<div>{this.state.errorCreateRoomName}</div>
							</div>
							<div className="input-group half-width">
								<label for="buy-in">Buy In</label>
								<input
									id="buy-in"
									value={this.state.buyIn}
									onChange={(e) =>
										this.handleInput(e.target.value, STATE_VAR_BUYIN)
									}
								/>
							</div>
							<div className="input-group half-width">
								<label for="big-blind">Big Blind</label>
								<input
									id="big-blind"
									value={this.state.bigBlind}
									onChange={(e) =>
										this.handleInput(
											e.target.value,
											STATE_VAR_BIGBLIND
										)
									}
								/>
							</div>
							<br />
							<div className="input-group half-width">
								<label for="small-blind">Small Blind</label>
								<input
									id="small-blind"
									value={this.state.smallBlind}
									onChange={(e) =>
										this.handleInput(
											e.target.value,
											STATE_VAR_SMALLBLIND
										)
									}
								/>
							</div>
							<div className="input-group half-width">
								<label for="ante">Ante</label>
								<input
									id="ante"
									value={this.state.ante}
									onChange={(e) =>
										this.handleInput(e.target.value, STATE_VAR_ANTE)
									}
								/>
							</div>
							<br />
							<div className="input-group">
								<label for="create-room-player-name">
									Your Player Name
								</label>
								<input
									id="create-room-player-name"
									value={this.state.createRoomPlayerName}
									onChange={(e) =>
										this.handleInput(
											e.target.value,
											STATE_VAR_CREATEPLAYERNAME
										)
									}
								/>
								<div>{this.state.errorCreateRoomPlayerName}</div>
							</div>
							<button
								onClick={() => {
									this.createRoom();
								}}
							>
								Create Room
							</button>
						</div>
						<div className="divider" />
					</Fragment>
				)}
			</div>
		);
	}
}
