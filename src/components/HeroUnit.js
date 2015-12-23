import React from "react";

class HeroUnit extends React.Component {

	render(){
		//console.log("Rendering")
		return (
			<div>
			<h2 className="title"> HumDrum </h2>
			<p> FE boilerplate code using babel + react or angular </p>
			</div>
		)
	}
}

/*export default React.createClass({

	render : function(){
		console.log("Rendering")
		return (
			<div>
			<h2 className="title"> HumDrum </h2>			
			</div>
		);	
	}
});*/

export default HeroUnit;