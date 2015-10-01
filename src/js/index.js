var app = angular.module('hEngine',['ui.bootstrap']);


angular.module('hEngine').controller('AccordionDemoCtrl',function($scope) {

  // BUTTONS ======================

  // define some random object and button values
  $scope.bigData = {};

  $scope.bigData.breakfast = false;
  $scope.bigData.lunch = false;
  $scope.bigData.dinner = false;

  // COLLAPSE =====================
  $scope.isCollapsed = false;

  $scope.$watch(function(){console.log(arguments)});

}); 

app.controller('UserProfileController',['$scope','$http',function($scope,$http){
  $scope.gh_handle = '';
  $scope.pic = '';
  
  $scope.fetchuser = function(){
    $http.get('http://localhost:3000/users/'+$scope.handle)
    .success(function(data,status,headers,config){
      $scope.gh_handle = data[0].gh_handle;
      $scope.pic = data[0].gh_pic_url;
      $scope.repo_count = data[0].repo_count;
      $scope.people = data[0].people;
    });
  };


}]);
        
