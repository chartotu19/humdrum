'use strict';

module.exports = function(grunt) {


  return {
    options: {
      separator: ';',
    },
    dist: {
      src: ['src/js/app.js', 'src/js/*.js', 'src/js/**/*.js'],
      dest: 'public/js/dist.js',
    },
  };
};