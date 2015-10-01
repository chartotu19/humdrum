module.exports = function(grunt){
	return {
		options: {
	          	entry: "./src/js/index.js",
			    //context:__dirname+'/src/main/js/',
			    output: {
			        path: './public/js/',        
			        filename: "dist.js"
			    },
			    module: {
			        loaders: [
			            //{ test: /\.css$/, loader: "style!css" },
			            //{ test: /\.js$/, loader: "style!css" }
			        ]
			    },
			    devtools: 'source-map',
			    resolve: {
			        modulesDirectories: ['node_modules']
			    }
	        },
	    "build-dev": {
			devtool: "sourcemap",
			debug: true
		}
    }
};



