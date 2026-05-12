export default {
  dependency: {
    platforms: {
      android: {
        packageImportPath:
          "import com.sparkfrost.SparkFrostPackage;\nimport com.sparktokenprimitives.SparkTokenPrimitivesPackage;",
        packageInstance:
          "new SparkFrostPackage(), new SparkTokenPrimitivesPackage()",
      },
    },
  },
};
