import React from "react";

export default function Player(props) {
	if (!props.table.Seats || props.table.Seats.length < props.seat + 1) {
		return null;
	}
	return (
		<div style={{ height: "100%", width: "100%", position: "relative" }}>
			{props.table.Seats &&
				props.table.Active.ID === props.table.Seats[props.seat].ID && (
					<div
						style={{
							position: "absolute",
							top: "0",
							right: "0",
							fontSize: "36px"
						}}
					>
						*
					</div>
				)}
			<p>IN POT: {props.table.Seats[props.seat].ChipsInPot}</p>
			<p>STACK: {props.table.Seats[props.seat].Chips}</p>
			<p>{props.table.Seats[props.seat].ID}</p>
			<p>
				{props.player.ID === props.table.Seats[props.seat].ID
					? props.player.Cards
					: props.table.Seats.Cards}
			</p>
		</div>
	);
}
