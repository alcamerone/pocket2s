import React from "react";
import Card from "./react-poker/Card.js";

const TABLE_STATUS_DONE = 2;

const intToDollars = (i) => {
	const dollars = i / 100;
	return `$${dollars.toFixed(2)}`;
};

export default function Player(props) {
	if (!props.table.Seats || props.table.Seats.length < props.seat + 1) {
		return null;
	}
	const isHero = props.player.ID === props.table.Seats[props.seat].ID;
	const cards = isHero
		? props.player.Cards
		: props.table.Seats[props.seat].Cards
		? props.table.Seats[props.seat].Cards
		: ["Xx", "Xx"];
	const roundOver = props.table.Status === TABLE_STATUS_DONE;

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
			<div
				className="player-info"
				style={{ marginTop: "5px", marginBottom: "10px" }}
			>
				<div style={{ marginBottom: "5px" }}>
					IN POT: {intToDollars(props.table.Seats[props.seat].ChipsInPot)}
				</div>
				<div style={{ marginBottom: "5px" }}>
					STACK: {intToDollars(props.table.Seats[props.seat].Chips)}
				</div>
				<div style={{ marginBottom: "5px" }}>
					{props.table.Seats[props.seat].ID}
				</div>
			</div>
			<div>
				{cards &&
					cards.map((card) => {
						return (
							<div
								style={{
									height: "60px",
									width: "45px",
									display: "inline-block"
								}}
							>
								<Card
									card={card}
									faceDown={!isHero && !roundOver}
									rotationY={0}
								/>
							</div>
						);
					})}
			</div>
		</div>
	);
}
