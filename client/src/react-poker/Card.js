import React, { PureComponent } from "react";
import CT from "../assets/cards/10C.svg";
import DT from "../assets/cards/10D.svg";
import HT from "../assets/cards/10H.svg";
import ST from "../assets/cards/10S.svg";
import C2 from "../assets/cards/2C.svg";
import D2 from "../assets/cards/2D.svg";
import H2 from "../assets/cards/2H.svg";
import S2 from "../assets/cards/2S.svg";
import C3 from "../assets/cards/3C.svg";
import D3 from "../assets/cards/3D.svg";
import H3 from "../assets/cards/3H.svg";
import S3 from "../assets/cards/3S.svg";
import C4 from "../assets/cards/4C.svg";
import D4 from "../assets/cards/4D.svg";
import H4 from "../assets/cards/4H.svg";
import S4 from "../assets/cards/4S.svg";
import C5 from "../assets/cards/5C.svg";
import D5 from "../assets/cards/5D.svg";
import H5 from "../assets/cards/5H.svg";
import S5 from "../assets/cards/5S.svg";
import C6 from "../assets/cards/6C.svg";
import D6 from "../assets/cards/6D.svg";
import H6 from "../assets/cards/6H.svg";
import S6 from "../assets/cards/6S.svg";
import C7 from "../assets/cards/7C.svg";
import D7 from "../assets/cards/7D.svg";
import H7 from "../assets/cards/7H.svg";
import S7 from "../assets/cards/7S.svg";
import C8 from "../assets/cards/8C.svg";
import D8 from "../assets/cards/8D.svg";
import H8 from "../assets/cards/8H.svg";
import S8 from "../assets/cards/8S.svg";
import C9 from "../assets/cards/9C.svg";
import D9 from "../assets/cards/9D.svg";
import H9 from "../assets/cards/9H.svg";
import S9 from "../assets/cards/9S.svg";
import CJ from "../assets/cards/JC.svg";
import DJ from "../assets/cards/JD.svg";
import HJ from "../assets/cards/JH.svg";
import SJ from "../assets/cards/JS.svg";
import CQ from "../assets/cards/QC.svg";
import DQ from "../assets/cards/QD.svg";
import HQ from "../assets/cards/QH.svg";
import SQ from "../assets/cards/QS.svg";
import CK from "../assets/cards/KC.svg";
import DK from "../assets/cards/KD.svg";
import HK from "../assets/cards/KH.svg";
import SK from "../assets/cards/KS.svg";
import CA from "../assets/cards/AC.svg";
import DA from "../assets/cards/AD.svg";
import HA from "../assets/cards/AH.svg";
import SA from "../assets/cards/AS.svg";
import back from "../assets/cards/back.svg";

const cardImgMap = {
  "2♣": C2,
  "2♦": D2,
  "2♥": H2,
  "2♠": S2,
  "3♣": C3,
  "3♦": D3,
  "3♥": H3,
  "3♠": S3,
  "4♣": C4,
  "4♦": D4,
  "4♥": H4,
  "4♠": S4,
  "5♣": C5,
  "5♦": D5,
  "5♥": H5,
  "5♠": S5,
  "6♣": C6,
  "6♦": D6,
  "6♥": H6,
  "6♠": S6,
  "7♣": C7,
  "7♦": D7,
  "7♥": H7,
  "7♠": S7,
  "8♣": C8,
  "8♦": D8,
  "8♥": H8,
  "8♠": S8,
  "9♣": C9,
  "9♦": D9,
  "9♥": H9,
  "9♠": S9,
  "T♣": CT,
  "T♦": DT,
  "T♥": HT,
  "T♠": ST,
  "J♣": CJ,
  "J♦": DJ,
  "J♥": HJ,
  "J♠": SJ,
  "Q♣": CQ,
  "Q♦": DQ,
  "Q♥": HQ,
  "Q♠": SQ,
  "K♣": CK,
  "K♦": DK,
  "K♥": HK,
  "K♠": SK,
  "A♣": CA,
  "A♦": DA,
  "A♥": HA,
  "A♠": SA
};

class Card extends PureComponent {
  shouldComponentUpdate(nextProps) {
    if (nextProps.rotationY !== this.props.rotationY) {
      return true;
    }

    if (nextProps.size !== this.props.size) {
      return true;
    }

    return false;
  }
  render() {
    const { card, faceDown, rotationY } = this.props;

    return (
      <div className="card" style={{ transform: `rotateY(${rotationY}deg)` }}>
        <img
          className={faceDown === true ? "front" : "back"}
          src={back}
          style={{ width: "100%", height: "100%" }}
          alt={card}
        />
        <img
          className={faceDown === true ? "back" : "front"}
          src={cardImgMap[card]}
          style={{ width: "100%", height: "100%" }}
          alt={card}
        />
      </div>
    );
  }
}

export default Card;
