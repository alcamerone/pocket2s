import React from "react";
import "./App.css";
import GameTable from "./GameTable.js";

function App() {
  return (
    <div
      className="App"
      style={{
        height: "100vh",
        width: "100vw",
        backgroundColor: "#333333",
        color: "lightgray"
      }}
    >
      <GameTable />
    </div>
  );
}

export default App;
