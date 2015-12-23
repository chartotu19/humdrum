module.exports = function(grunt){
	return {
		option: {
		  entry: './src/index.js',
		  output: { path: './public/js', filename: 'bundle.js' },
		  module: {
		    loaders: [
		      {
		        test: /.jsx?$/,
		        loader: 'babel-loader',
		        exclude: /node_modules/,
		        query: {
		          presets: ['es2015','react',"stage-0"]
		        }
		      }
		    ]
		  	},
		  	devtools: 'source-map',
		    resolve: {
		    	modulesDirectories: ['node_modules']
		    }
		}
    }
};
