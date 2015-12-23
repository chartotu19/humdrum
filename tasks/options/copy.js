'use strict';
module.exports = function(grunt) {
  return {
    main: {
      files: [{
        cwd: '',
        src: 'src/index.html',
        dest: 'public/index.html'
      }]
    }
  };
};
