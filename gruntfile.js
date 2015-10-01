'use strict';

module.exports = function(grunt) {

  /*
    Loading all tasks. Please place custom tasks in the tasks/ folder.
    This module will read the dependencies/devDependencies/peerDependencies/optionalDependencies in your package.json 
    and load grunt tasks that match the provided patterns.
  */
  require('load-grunt-tasks')(grunt, {
    pattern: ['grunt-*', 'assemble'] //Update pattern to accomodate your new tasks.
  });
  grunt.loadTasks('tasks');

  /*
    Loading helper functions.
  */
  require('./grunt-helper.js')(grunt);

  /*
      Grunt build configuration block.
  */
  var config = {
    pkg: grunt.file.readJSON('package.json')
  };

  grunt.util._.extend(config, grunt.loadConfig('./tasks/options/'));

  // Setup the project configuration.
  grunt.initConfig(config);

  //grunt.registerTask('default', ['clean', 'config', 'sass', 'concat', 'handlebars', 'assemble', 'copy', 'connect', 'watch']);
  grunt.registerTask('default', ['clean:dev', 'concat','webpack','copy','connect']);


};
