import React, { Component, Fragment } from "react";
import config from "./config.js";

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
			buyInInt: 2000,
			bigBlind: "0.20",
			bigBlindInt: 20,
			smallBlind: "0.10",
			smallBlindInt: 10,
			ante: "0.00",
			anteInt: 0
		};
	}

	handleStringInput(input) {
		const match = input.match(/[A-Za-z0-9-_]*/);
		if (!match) {
			return;
		}
		return match;
	}

	handleFloatInput(input) {
		const val = parseFloat(input);
		if (isNaN(val)) {
			return [null, null];
		}
		const intVal = Math.round(val * 100);
		return [val, intVal];
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
				BuyIn: this.state.buyInInt,
				BigBlind: this.state.bigBlindInt,
				SmallBlind: this.state.smallBlindInt,
				Ante: this.state.anteInt
			})
		})
			.then((resp) => {
				if (resp.ok) {
					window.open(
						`${config.appUrl}/${this.state.createRoomName}/${this.state.createRoomPlayerName}`,
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
						<label htmlFor="player-name">Player Name</label>
						<input
							id="player-name"
							value={this.state.playerName}
							onChange={(e) => {
								const playerName = this.handleStringInput(e.target.value);
								if (playerName) {
									this.setState({ playerName });
								}
							}}
						/>
						<div>{this.state.errorPlayerName}</div>
					</div>
					<div className="input-group" style={{ display: "inline-block" }}>
						<label htmlFor="room-name">Room Name</label>
						<input
							id="room-name"
							value={this.state.roomName}
							onChange={(e) => {
								const roomName = this.handleStringInput(e.target.value);
								if (roomName) {
									this.setState({ roomName });
								}
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
								<label htmlFor="create-room-name">Room Name</label>
								<input
									id="create-room-name"
									value={this.state.createRoomName}
									onChange={(e) => {
										const createRoomName = this.handleStringInput(
											e.target.value
										);
										if (createRoomName) {
											this.setState({ createRoomName });
										}
									}}
								/>
								<div>{this.state.errorCreateRoomName}</div>
							</div>
							<div className="input-group half-width">
								<label htmlFor="buy-in">Buy In</label>
								<input
									type="number"
									id="buy-in"
									step="0.01"
									value={this.state.buyIn}
									onChange={(e) => {
										const [buyIn, buyInInt] = this.handleFloatInput(
											e.target.value
										);
										if (buyIn && buyInInt) {
											this.setState({ buyIn, buyInInt });
										}
									}}
								/>
							</div>
							<div className="input-group half-width">
								<label htmlFor="big-blind">Big Blind</label>
								<input
									type="number"
									id="big-blind"
									step="0.01"
									value={this.state.bigBlind}
									onChange={(e) => {
										const [
											bigBlind,
											bigBlindInt
										] = this.handleFloatInput(e.target.value);
										if (bigBlind && bigBlindInt) {
											this.setState({ bigBlind, bigBlindInt });
										}
									}}
								/>
							</div>
							<br />
							<div className="input-group half-width">
								<label htmlFor="small-blind">Small Blind</label>
								<input
									type="number"
									id="small-blind"
									value={this.state.smallBlind}
									step="0.01"
									onChange={(e) => {
										const [
											smallBlind,
											smallBlindInt
										] = this.handleFloatInput(e.target.value);
										if (smallBlind && smallBlindInt) {
											this.setState({ smallBlind, smallBlindInt });
										}
									}}
								/>
							</div>
							<div className="input-group half-width">
								<label htmlFor="ante">Ante</label>
								<input
									type="number"
									id="ante"
									value={this.state.ante}
									step="0.01"
									onChange={(e) => {
										const [ante, anteInt] = this.handleInput(
											e.target.value
										);
										if (ante && anteInt) {
											this.setState({ ante, anteInt });
										}
									}}
								/>
							</div>
							<br />
							<div className="input-group">
								<label htmlFor="create-room-player-name">
									Your Player Name
								</label>
								<input
									id="create-room-player-name"
									value={this.state.createRoomPlayerName}
									onChange={(e) => {
										const createRoomPlayerName = this.handleStringInput(
											e.target.value
										);
										if (createRoomPlayerName) {
											this.setState({ createRoomPlayerName });
										}
									}}
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
