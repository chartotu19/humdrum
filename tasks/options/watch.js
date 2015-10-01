module.exports = function(grunt){
	return {
    options: {
      livereload: 1337
        },
        JavaScript : {
          spwan : true,
          files : [
              './src/js/**/*.js',
              '!./src/js/**/*.min.js'
          ],
          tasks : [ 'webpack:build-dev' ]
        },
        sass : {
          files : [ './src/main/scss/**/*.scss' ],
          tasks : [ 'sass' ]
        }
    }
  };
