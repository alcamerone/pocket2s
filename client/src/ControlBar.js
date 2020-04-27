import React, { Component } from "react";

const TABLE_STATUS_DONE = 2;

const MSG_TYPE_READY = 2;
const MSG_TYPE_PLAYER_ACTION = 6;

const ACTION_FOLD = 0;
const ACTION_CHECK = 1;
const ACTION_CALL = 2;
const ACTION_BET = 3;
const ACTION_RAISE = 4;
const ACTION_ALLIN = 5;

export default class ControlBar extends Component {
	constructor(props) {
		super(props);
		this.state = {
			bet: 0,
			ready: false
		};
	}

	componentDidUpdate(prevProps) {
		if (
			this.props.table &&
			this.props.table.Status === TABLE_STATUS_DONE &&
			!prevProps.table.Status === TABLE_STATUS_DONE
		) {
			this.setState({ ready: false });
		}
	}

	sendAction(action, conn) {
		conn.send(JSON.stringify(action));
	}

	render() {
		const myTurn = this.props.table
			? this.props.table.Active.ID === this.props.player.ID
			: false;

		if (!this.props.connected) {
			return null;
		}
		if (!this.state.ready) {
			return (
				<div
					style={{
						height: "60px",
						width: "960px",
						backgroundColor: "#444444",
						margin: "auto"
					}}
				>
					<button
						style={{ height: "33%", width: "10%", margin: "auto" }}
						onClick={() => {
							this.sendAction({ Type: MSG_TYPE_READY }, this.props.conn);
							this.setState({ ready: true });
						}}
					>
						READY
					</button>
				</div>
			);
		}
		if (this.state.ready && !this.props.table) {
			return (
				<div
					style={{
						height: "60px",
						width: "960px",
						textAlign: "center",
						backgroundColor: "#444444",
						margin: "auto"
					}}
				>
					Waiting for other players...
				</div>
			);
		}
		return (
			<div
				style={{
					height: "60px",
					width: "960px",
					backgroundColor: "#444444",
					margin: "auto"
				}}
			>
				<div style={{ height: "100%", width: "25%", display: "inline-block" }}>
					<button
						style={{ height: "33%", width: "66%", margin: "auto" }}
						disabled={!myTurn}
						onClick={() =>
							this.sendAction(
								{
									Type: MSG_TYPE_PLAYER_ACTION,
									Action: { Type: ACTION_FOLD }
								},
								this.props.conn
							)
						}
					>
						FOLD
					</button>
				</div>
				<div style={{ height: "100%", width: "25%", display: "inline-block" }}>
					<button
						style={{ height: "33%", width: "66%", margin: "auto" }}
						disabled={!myTurn}
						onClick={() =>
							this.sendAction(
								{
									Type: MSG_TYPE_PLAYER_ACTION,
									Action: {
										Type:
											this.props.table.Owed !== 0
												? ACTION_CALL
												: ACTION_CHECK
									}
								},
								this.props.conn
							)
						}
					>
						{this.props.table.Owed !== 0 ? "CALL" : "CHECK"}
					</button>
				</div>
				<div style={{ height: "100%", width: "25%", display: "inline-block" }}>
					<button
						style={{
							height: "33%",
							width: "66%",
							margin: "auto",
							display: "block"
						}}
						disabled={
							!myTurn || this.props.table.Owed > this.props.player.Chips
						}
						onClick={() => {
							let thisBet = this.state.bet;
							if (thisBet < this.props.table.Options.Stakes.BigBlind) {
								thisBet = this.props.table.Options.Stakes.BigBlind;
							}
							this.sendAction(
								{
									Type: MSG_TYPE_PLAYER_ACTION,
									Action: {
										Type:
											this.props.table.Owed !== 0
												? ACTION_RAISE
												: ACTION_BET,
										Chips: thisBet
									}
								},
								this.props.conn
							);
							this.setState({
								bet: this.props.table.Options.Stakes.BigBlind
							});
						}}
					>
						{this.props.table.Owed !== 0 ? "RAISE" : "BET"}
					</button>
					<input
						type="range"
						style={{ width: "66%" }}
						value={this.state.bet}
						max={this.props.player.Chips}
						min={this.props.table.Options.Stakes.BigBlind}
						step={this.props.table.Options.Stakes.BigBlind}
						onChange={(e) => {
							const bet = parseInt(e.target.value, 10);
							this.setState({ bet });
						}}
					/>
					<input type="text" value={this.state.bet} disabled />
				</div>
				<div
					style={{ height: "100%", width: "25%", display: "inline-block" }}
					onClick={() =>
						this.sendAction(
							{
								Type: MSG_TYPE_PLAYER_ACTION,
								Action: { Type: ACTION_ALLIN }
							},
							this.props.conn
						)
					}
				>
					<button
						disabled={
							!myTurn || this.props.table.Owed > this.props.player.Chips
						}
						style={{ height: "33%", width: "66%", margin: "auto" }}
					>
						ALL IN
					</button>
				</div>
			</div>
		);
	}
}
