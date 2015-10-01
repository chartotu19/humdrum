/*
	Append helper methods that maybe useful across grunt tasks and configs.
*/

module.exports = function(grunt){

	// extract just the file name from the full path
  	grunt.fileName = function (filePath) {
    	var fileParts = filePath.split('/');
     	var fileNameParts = fileParts[fileParts.length - 1].split('.');
     	return fileNameParts[0];
  	};

  	grunt.loadConfig = function(path) {
  		var glob = require('glob');
  		var object = {};
  		var key;
 
  		glob.sync('*', {cwd: path}).forEach(function(option) {
    		key = option.replace(/\.js$/,'');
    		object[key] = require(path + option)(grunt);
  		});
 
  		return object;
	}
}